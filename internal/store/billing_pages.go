package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (t *Tenant) billingMutationTx(ctx context.Context, a capability.Authorized, write func(*storeTx, capability.Principal) error) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		return t.teamMutationInTx(ctx, tx, a, "billingMutationTx", string(t.team), func(tx *storeTx, p capability.Principal) error {
			if _, err := lockLedgerTx(ctx, tx, t.team); err != nil {
				return err
			}
			var deleting int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_deletions WHERE slug=? AND state=?`, t.team, TeamDeletionPending).Scan(&deleting); err != nil {
				return err
			}
			if deleting > 0 {
				return ErrTeamBeingDeleted
			}
			return write(tx, p)
		})
	})
}

// PrepareAuthorizedBillingPage reserves one immutable financial operation under current authority.
func (t *Tenant) PrepareAuthorizedBillingPage(ctx context.Context, a capability.Authorized, kind capability.BillingPageKind, amount int64, key string) (capability.BillingPage, error) {
	var page capability.BillingPage
	err := t.billingMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		if kind == capability.BillingPageCredits && amount > MaxCardPaymentCents {
			return fmt.Errorf("%w: each payment is at most $%d; contact us through the billing request form or sparkwing billing request-tier for larger payments", ErrInvalidInput, MaxCardPaymentCents/100)
		}
		if strings.TrimSpace(key) == "" {
			return ErrInvalidInput
		}
		var data []byte
		var mode capability.PaymentMode
		err := tx.QueryRowContext(ctx, `SELECT page_json,payment_mode FROM billing_pages WHERE team=? AND account_id=? AND request_key=?`, t.team, p.Capability.Subject.ID, key).Scan(&data, &mode)
		if err == nil {
			if err := configuredPaymentModeTx(ctx, tx, mode); err != nil {
				return err
			}
			if err := json.Unmarshal(data, &page); err != nil {
				return err
			}
			if page.Kind != kind || kind == capability.BillingPageCredits && page.AmountCents != amount || kind != capability.BillingPageCredits && amount != 0 {
				return authorityError(apierr.Conflict, "idempotency key names another billing request")
			}
			if !tx.now(ctx).Before(page.ExpiresAt) {
				return authorityError(apierr.Conflict, "payment page reservation expired")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		mode, err = paymentModeTx(ctx, tx)
		if err != nil {
			return err
		}
		page = capability.BillingPage{Kind: kind, Team: string(t.team), ExpiresAt: tx.now(ctx).Add(billingPageLife)}
		if err := tx.QueryRowContext(ctx, `SELECT t.card_customer,a.email FROM teams t JOIN accounts a ON a.id=? WHERE t.name=?`, p.Capability.Subject.ID, t.team).Scan(&page.Customer, &page.Email); err != nil {
			return err
		}
		switch kind {
		case capability.BillingPageCredits:
			if amount < MinCardChargeCents {
				return ErrInvalidInput
			}
			page.AmountCents = amount
			page.ReservationID, err = t.openCreditCheckoutTx(ctx, tx, amount*MicroCreditsPerCent, tx.now(ctx), billingPageLife)
		case capability.BillingPageCard:
			if amount != 0 {
				return ErrInvalidInput
			}
			page.ReservationID, err = newCreditID("setup")
		default:
			return ErrInvalidInput
		}
		if err != nil {
			return err
		}
		data, err = json.Marshal(page)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO billing_pages(id,team,account_id,request_key,payment_mode,page_json) VALUES (?,?,?,?,?,?)`, page.ReservationID, t.team, p.Capability.Subject.ID, key, mode, data)
		return err
	})
	if err != nil {
		return capability.BillingPage{}, err
	}
	return page, nil
}

func (t *Tenant) billingPageTx(ctx context.Context, tx *storeTx, p capability.Principal, id string) (capability.BillingPage, capability.PaymentMode, error) {
	var data []byte
	var mode capability.PaymentMode
	var page capability.BillingPage
	err := tx.QueryRowContext(ctx, `SELECT page_json,payment_mode FROM billing_pages WHERE id=? AND team=? AND account_id=?`+tx.forUpdate(), id, t.team, p.Capability.Subject.ID).Scan(&data, &mode)
	if err != nil {
		return page, mode, err
	}
	err = json.Unmarshal(data, &page)
	if err == nil {
		err = configuredPaymentModeTx(ctx, tx, mode)
	}
	return page, mode, err
}

// CompleteAuthorizedBillingPage binds provider facts to the caller's committed reservation.
func (t *Tenant) CompleteAuthorizedBillingPage(ctx context.Context, a capability.Authorized, page capability.BillingPage) error {
	return t.billingMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		prior, mode, err := t.billingPageTx(ctx, tx, p, page.ReservationID)
		if err != nil {
			return err
		}
		expected := prior
		expected.URL, expected.ProviderID, expected.ExpiresAt = page.URL, page.ProviderID, page.ExpiresAt
		if prior.Kind == capability.BillingPageCard && prior.Customer == "" {
			expected.Customer = page.Customer
		}
		u, urlErr := url.Parse(page.URL)
		if !reflect.DeepEqual(expected, page) || page.ProviderID == "" || urlErr != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || page.ExpiresAt.After(prior.ExpiresAt) || !page.ExpiresAt.After(tx.now(ctx)) {
			return ErrInvalidInput
		}
		if prior.URL != "" {
			if !reflect.DeepEqual(prior, page) {
				return authorityError(apierr.Conflict, "payment page is already completed")
			}
			return nil
		}
		switch page.Kind {
		case capability.BillingPageCredits:
			if page.ProviderID == page.ReservationID {
				return ErrInvalidInput
			}
			var result sql.Result
			result, err = tx.ExecContext(ctx, `UPDATE credit_checkouts SET session_id=?,expires_at=? WHERE team=? AND id=? AND session_id=id AND paid_at IS NULL AND closed_at IS NULL`, page.ProviderID, page.ExpiresAt.UnixNano(), t.team, page.ReservationID)
			if err == nil {
				err = requireOneRow(result, "checkout page binding")
			}
		case capability.BillingPageCard:
			if page.Customer == "" {
				return ErrInvalidInput
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES (?,?,'setup_session',?,?)`, mode, page.ProviderID, t.team, page.Customer)
		default:
			return ErrInvalidInput
		}
		if err != nil {
			return err
		}
		data, err := json.Marshal(page)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE billing_pages SET page_json=?,failed_at=NULL WHERE team=? AND id=?`, data, t.team, page.ReservationID)
		return err
	})
}

// RecordAuthorizedBillingPageFailure records ambiguous delivery without releasing its reservation.
func (t *Tenant) RecordAuthorizedBillingPageFailure(ctx context.Context, a capability.Authorized, page capability.BillingPage) error {
	return t.billingMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		prior, _, err := t.billingPageTx(ctx, tx, p, page.ReservationID)
		if err != nil {
			return err
		}
		if prior.Kind != page.Kind || prior.Team != page.Team || prior.AmountCents != page.AmountCents {
			return ErrInvalidInput
		}
		_, err = tx.ExecContext(ctx, `UPDATE billing_pages SET failed_at=? WHERE team=? AND id=?`, tx.now(ctx).UnixNano(), t.team, page.ReservationID)
		return err
	})
}

// safety: this outlives the 31-minute Checkout session a page opens, after which the session can no longer be paid.
const billingPageLife = 35 * time.Minute

// SetAuthorizedRefill commits the team's automatic refill settings under current team administration.
func (t *Tenant) SetAuthorizedRefill(ctx context.Context, a capability.Authorized, s RefillSettings) error {
	return t.billingMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		return setRefillTx(ctx, tx, t.team, s, auditPrincipalActor(p))
	})
}
