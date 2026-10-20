package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func billingPageFixture(t *testing.T) (*Store, *Tenant, capability.Authorized) {
	t.Helper()
	s := authorityStore(t)
	if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentTest); err != nil {
		t.Fatal(err)
	}
	actor := authorityMember(t, s, "owner", RoleOwner)
	return s, &Tenant{s: s, team: "a"}, teamAuthorization(t, s, actor, "a", "POST", "/api/v1/team/billing/checkout")
}

func TestBillingPagesRecheckAuthorityProperty(t *testing.T) {
	property := func(change uint8, suffix uint32) bool {
		s, tenant, a := billingPageFixture(t)
		page, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 2000, "key")
		if err != nil {
			t.Fatal(err)
		}
		switch change % 7 {
		case 0:
			a.Target.Team = fmt.Sprint("foreign-", suffix)
		case 1:
			rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a'`)
		case 2:
			rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE team='a'`)
		case 3:
			rewriteSchemaExec(t, s.db, `DELETE FROM sessions WHERE hash='owner'`)
		case 4:
			a.Principal.Authority.ID = fmt.Sprint("foreign-", suffix)
		case 5:
			a.Access.Grants = []capability.Grant{capability.RunsRead}
		case 6:
			a.Target.RunID = "forged-run"
		}
		before := billingPageRows(t, s)
		_, prepare := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 2000, "another")
		page.URL, page.ProviderID = "https://checkout.test/page", "cs_test"
		complete := tenant.CompleteAuthorizedBillingPage(t.Context(), a, page)
		failure := tenant.RecordAuthorizedBillingPageFailure(t.Context(), a, page)
		refill := tenant.SetAuthorizedRefill(t.Context(), a, RefillSettings{ThresholdCents: 2000})
		_, standing := tenant.RequestAuthorizedStanding(t.Context(), a, "High", "lost authority")
		var requests int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM standing_requests`).Scan(&requests); err != nil {
			t.Fatal(err)
		}
		return prepare != nil && complete != nil && failure != nil && refill != nil && standing != nil && requests == 0 && reflect.DeepEqual(before, billingPageRows(t, s))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 28}); err != nil {
		t.Fatal(err)
	}
}

func billingPageRows(t *testing.T, s *Store) []string {
	t.Helper()
	var page, checkout string
	var threshold int64
	if err := s.db.QueryRow(`SELECT CAST(page_json AS TEXT)||COALESCE(CAST(failed_at AS TEXT),'') FROM billing_pages`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT session_id FROM credit_checkouts`).Scan(&checkout); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT refill_threshold_cents FROM teams WHERE name='a'`).Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	return []string{page, checkout, fmt.Sprint(threshold)}
}

func TestBillingPagesImmutableReservationProperty(t *testing.T) {
	property := func(change uint8, suffix uint32) bool {
		s, tenant, a := billingPageFixture(t)
		page, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 2000, "key")
		if err != nil {
			t.Fatal(err)
		}
		page.URL, page.ProviderID = "https://checkout.test/page", "cs_test"
		switch change % 10 {
		case 0:
			page.Team = fmt.Sprint("foreign-", suffix)
		case 1:
			page.AmountCents += int64(suffix) + 1
		case 2:
			page.Customer = "forged"
		case 3:
			page.Email = "forged@example.test"
		case 4:
			page.AmountCents--
		case 5:
			page.Email = ""
		case 6:
			page.Kind = capability.BillingPageCard
		case 7:
			page.ExpiresAt = page.ExpiresAt.Add(time.Second)
		case 8:
			page.URL = "https://user:password@checkout.test/page"
		case 9:
			page.ReservationID = fmt.Sprint("foreign-", suffix)
		}
		before := billingPageRows(t, s)
		err = tenant.CompleteAuthorizedBillingPage(t.Context(), a, page)
		return err != nil && reflect.DeepEqual(before, billingPageRows(t, s))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
		t.Fatal(err)
	}
}

func TestBillingPagesFailureRetainsReservationAndProviderBinding(t *testing.T) {
	s, tenant, a := billingPageFixture(t)
	original, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 2000, "key")
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.RecordAuthorizedBillingPageFailure(t.Context(), a, original); err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 2000, "key")
	if err != nil || replay != original {
		t.Fatalf("replay=%+v: %v", replay, err)
	}
	replay.URL, replay.ProviderID = "https://checkout.test/page", "cs_test"
	for range 2 {
		if err := tenant.CompleteAuthorizedBillingPage(t.Context(), a, replay); err != nil {
			t.Fatal(err)
		}
	}
	replay.ProviderID = "cs_changed"
	if err := tenant.CompleteAuthorizedBillingPage(t.Context(), a, replay); err == nil {
		t.Fatal("replaced provider")
	}
	if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 2000, "key"); err == nil {
		t.Fatal("mode changed")
	}
}

func TestBillingPagesRollbackProviderBindingOnPersistenceFailure(t *testing.T) {
	s, tenant, a := billingPageFixture(t)
	page, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 2000, "key")
	if err != nil {
		t.Fatal(err)
	}
	page.ProviderID, page.URL = "cs_native", "https://checkout.test/page"
	before := billingPageRows(t, s)
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER fail_page BEFORE UPDATE ON billing_pages BEGIN SELECT RAISE(ABORT,'page unavailable'); END`)
	if err := tenant.CompleteAuthorizedBillingPage(t.Context(), a, page); err == nil {
		t.Fatal("partial completion succeeded")
	}
	if !reflect.DeepEqual(before, billingPageRows(t, s)) {
		t.Fatal("provider binding escaped rollback")
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER fail_page`)
	if err := tenant.CompleteAuthorizedBillingPage(t.Context(), a, page); err != nil {
		t.Fatal(err)
	}
}

func TestBillingPagesAccountRemovalAndTenantPurge(t *testing.T) {
	s, tenant, a := billingPageFixture(t)
	page, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCard, 0, "key")
	if err != nil {
		t.Fatal(err)
	}
	page.ProviderID, page.Customer, page.URL = "cs_setup", "cus_native", "https://checkout.test/page"
	if err := tenant.CompleteAuthorizedBillingPage(t.Context(), a, page); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `DELETE FROM sessions WHERE account_id='owner'`)
	rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE account_id='owner'`)
	rewriteSchemaExec(t, s.db, `DELETE FROM accounts WHERE id='owner'`)
	if err := s.AsOperator().deleteTenantRows(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM teams WHERE name='a'`); err != nil {
		t.Fatal(err)
	}
}

func TestBillingPagesConcurrentReplayReservesOnce(t *testing.T) {
	s, tenant, a := billingPageFixture(t)
	type result struct {
		page capability.BillingPage
		err  error
	}
	results := make(chan result, 8)
	for range 8 {
		go func() {
			p, e := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 2000, "key")
			results <- result{p, e}
		}()
	}
	var first capability.BillingPage
	for range 8 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if first.ReservationID == "" {
			first = r.page
		}
		if r.page != first {
			t.Fatal("duplicate reservation")
		}
	}
	for _, table := range []string{"credit_checkouts", "billing_pages"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE team='a'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s=%d: %v", table, n, err)
		}
	}
}

func TestBillingPaymentModeRefusesInvalidConfigurationProperty(t *testing.T) {
	property := func(raw []byte) bool {
		s, _, _ := billingPageFixture(t)
		mode := capability.PaymentMode(raw)
		if mode == capability.PaymentTest || mode == capability.PaymentLive {
			return true
		}
		err := s.AsOperator().SetPaymentMode(t.Context(), mode)
		var saved []byte
		if readErr := s.db.QueryRow(`SELECT value FROM settings WHERE key='payment_mode'`).Scan(&saved); readErr != nil {
			t.Fatal(readErr)
		}
		return err != nil && string(saved) == `"test"`
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}

func TestBillingPagesCapEachPaymentIncludingHighStanding(t *testing.T) {
	for _, high := range []bool{false, true} {
		t.Run(fmt.Sprint("High=", high), func(t *testing.T) {
			_, tenant, a := billingPageFixture(t)
			if high {
				if _, _, err := tenant.TestOnlySetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "ops", Reason: "reviewed"}, tenant.s.now()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, MaxCardPaymentCents, "max"); err != nil {
				t.Fatalf("maximum payment refused: %v", err)
			}
			if _, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, MaxCardPaymentCents+1, "above"); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "contact us") || !strings.Contains(err.Error(), "larger payments") {
				t.Fatalf("oversized payment: %v", err)
			}
			var n int
			if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM credit_checkouts WHERE team='a'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("checkout count=%d: %v", n, err)
			}
		})
	}
}
