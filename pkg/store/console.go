package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Console commits actions under the configured operator account gate.
type Console struct {
	s       *Store
	allowed func(string) bool
}

// AsConsole binds the live operator configuration to console transactions.
func (s *Store) AsConsole(allowed func(string) bool) *Console {
	return &Console{s: s, allowed: allowed}
}

func consoleWrite[T any](ctx context.Context, c *Console, a capability.Authorized, write func(*storeTx, string) (T, error)) (T, error) {
	var zero T
	if c == nil || c.s == nil || c.allowed == nil || a.Principal.Class != capability.CredentialConsole || a.Principal.Capability.Subject.Kind != capability.SubjectOperator {
		return zero, authorityError(apierr.Forbidden, "direct operator session required")
	}
	tx, err := c.s.beginTx(ctx)
	if err != nil {
		return zero, err
	}
	defer rollbackOrLog(tx)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return zero, err
	}
	p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
	if err != nil {
		return zero, err
	}
	if !capability.ConsoleAuthority(p, c.allowed(p.Capability.Subject.ID), tx.now(), 0) || p.Capability.Subject.ID != a.Principal.Capability.Subject.ID || c.s.consolePolicy != nil && !c.s.consolePolicy(p, tx.now()) {
		return zero, authorityError(apierr.Forbidden, "direct operator session required")
	}
	out, err := write(tx, p.Capability.Subject.ID)
	if err != nil {
		return zero, err
	}
	if !capability.ConsoleAuthority(p, c.allowed(p.Capability.Subject.ID), tx.now(), 0) || c.s.consolePolicy != nil && !c.s.consolePolicy(p, tx.now()) {
		return zero, authorityError(apierr.Forbidden, "operator account was removed")
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return out, nil
}

func consoleTeamTx(ctx context.Context, tx *storeTx, name string) (*Tenant, error) {
	if name == "" || string(NormalizeTeam(Team(name))) != name {
		return nil, bindingRefusal()
	}
	t := &Tenant{s: tx.store, team: Team(name)}
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return nil, err
	}
	return t, nil
}

func consoleReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 500 {
		return "", authorityError(apierr.BadRequest, "reason is required, up to 500 characters")
	}
	return reason, nil
}

// ConsoleGrantCredit grants free credits once under the current operator's identity.
func (c *Console) ConsoleGrantCredit(ctx context.Context, a capability.Authorized, req CreditGrantRequest) (CreditGrantResult, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor string) (CreditGrantResult, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if req.Kind != CreditGrantFree || !strings.HasPrefix(req.Reference, "console:") || len(req.Reference) <= len("console:") || req.Reverses != "" || req.AmountMicro <= 0 || req.AmountMicro > MaxPurchaseLimitCents*MicroCreditsPerCent {
			return CreditGrantResult{}, ErrInvalidInput
		}
		req.Reason, err = consoleReason(req.Reason)
		if err != nil {
			return CreditGrantResult{}, err
		}
		req.CreatedBy = actor
		return recordCreditGrantTx(ctx, tx, t.team, req)
	})
}

// ConsoleSetBillingTrust records trust and its audit under current operator authority.
func (c *Console) ConsoleSetBillingTrust(ctx context.Context, a capability.Authorized, change BillingTrustChange) (BillingStanding, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor string) (BillingStanding, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return BillingStanding{}, err
		}
		change.Reason, err = consoleReason(change.Reason)
		if err != nil {
			return BillingStanding{}, err
		}
		change.Actor = actor
		_, after, err := t.setBillingTrustTx(ctx, tx, change, tx.now())
		return after, err
	})
}

// ConsoleFreeze places a new operator hold and returns the committed freeze snapshot.
func (c *Console) ConsoleFreeze(ctx context.Context, a capability.Authorized, reason string) (TeamCreditFreeze, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor string) (TeamCreditFreeze, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		reason, err = consoleReason(reason)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		id, err := newCreditID("hold")
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		if _, err := holdCreditFreezeTx(ctx, tx, t.team, OperatorHoldPrefix+id, "", reason, actor, tx.now()); err != nil {
			return TeamCreditFreeze{}, err
		}
		return teamCreditFreezeTx(ctx, tx, t.team)
	})
}

// ConsoleUnfreeze releases a named hold or only operator holds when none is named.
func (c *Console) ConsoleUnfreeze(ctx context.Context, a capability.Authorized, hold, reason string) (TeamCreditFreeze, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor string) (TeamCreditFreeze, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		reason, err = consoleReason(reason)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		hold = strings.TrimSpace(hold)
		count, err := releaseCreditFreezesTx(ctx, tx, t.team, hold, hold == "", actor, reason, tx.now())
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		if count == 0 && hold != "" {
			return TeamCreditFreeze{}, ErrNotFound
		}
		return teamCreditFreezeTx(ctx, tx, t.team)
	})
}

// ConsoleDeleteTeam queues deletion after checking money in flight under current authority.
func (c *Console) ConsoleDeleteTeam(ctx context.Context, a capability.Authorized) (TeamDeletion, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor string) (TeamDeletion, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return TeamDeletion{}, err
		}
		if _, err := t.markForDeletionTx(ctx, tx, actor, tx.now()); err != nil {
			return TeamDeletion{}, err
		}
		return teamDeletionTx(ctx, tx, t.team)
	})
}

// ConsoleDeleteAccount resolves the selected account and erases it in the authorized transaction.
func (c *Console) ConsoleDeleteAccount(ctx context.Context, a capability.Authorized) (AccountDeletion, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, _ string) (AccountDeletion, error) {
		account, err := accountTx(ctx, tx, a.Target.AccountID)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
			account, err = accountByEmailTx(ctx, tx, a.Target.AccountID)
		}
		if err != nil {
			return AccountDeletion{}, err
		}
		return c.s.deleteAccountTx(ctx, tx, account.ID, tx.now())
	})
}

// ConsoleSetCreditSettings preserves installed price tables under current operator authority.
func (c *Console) ConsoleSetCreditSettings(ctx context.Context, a capability.Authorized, up CreditSettingsUpdate) (CreditSettings, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, _ string) (CreditSettings, error) {
		if err := advisoryLockTx(ctx, tx, "sparkwing/credit-settings"); err != nil {
			return CreditSettings{}, err
		}
		return setOperatorCreditSettingsTx(ctx, tx, up)
	})
}

// ConsoleUnbindGitHubInstallation removes the current installation and its dependent schedules atomically.
func (c *Console) ConsoleUnbindGitHubInstallation(ctx context.Context, a capability.Authorized, installation int64) (Team, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, _ string) (Team, error) {
		if installation <= 0 {
			return "", ErrInvalidInput
		}
		var team Team
		err := tx.QueryRowContext(ctx, `SELECT team FROM github_app_installations WHERE installation_id=?`+tx.forUpdate(), installation).Scan(&team)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", err
		}
		t := &Tenant{s: c.s, team: team}
		return team, t.unbindGitHubAppInstallationTx(ctx, tx, installation)
	})
}

// ConsoleApproveWaitlist admits one stable batch per request key under current operator authority.
func (c *Console) ConsoleApproveWaitlist(ctx context.Context, a capability.Authorized, ids []string, oldest int, key string) ([]Account, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor string) ([]Account, error) {
		if key == "" || oldest < 0 || (len(ids) > 0) == (oldest > 0) {
			return nil, ErrInvalidInput
		}
		request, err := json.Marshal(struct {
			IDs    []string
			Oldest int
		}{ids, oldest})
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(request)
		identity, err := json.Marshal([]string{actor, key})
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(identity)
		name := "console-approval:" + hex.EncodeToString(sum[:])
		var body []byte
		err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, name).Scan(&body)
		var replay [32]byte
		if err == nil {
			if err := json.Unmarshal(body, &replay); err != nil {
				return nil, err
			}
			if replay != digest {
				return nil, authorityError(apierr.Conflict, "idempotency key names another approval")
			}
			return []Account{}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if oldest > 0 {
			rows, err := tx.QueryContext(ctx, `SELECT id FROM accounts WHERE waitlisted_at<>0 ORDER BY waitlisted_at,id LIMIT ?`, oldest)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				ids = append(ids, id)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return nil, err
			}
		}
		replay = digest
		out := []Account{}
		for _, id := range ids {
			acct, changed, err := approveOneTx(ctx, tx, id, actor, tx.now())
			if err != nil {
				return nil, err
			}
			if changed {
				out = append(out, acct)
			}
		}
		body, err = json.Marshal(replay)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?)`, name, body); err != nil {
			return nil, err
		}
		return out, nil
	})
}
