package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func billingModelCheckout(t *testing.T, s *store.Store, tenant *store.Tenant, provider string, cents int64) string {
	t.Helper()
	owner, session := "billing-owner-"+string(tenant.Team()), "billing-session-"+string(tenant.Team())
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1) ON CONFLICT DO NOTHING`, owner, owner+"@example.test")
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES (?,?,'owner',1) ON CONFLICT DO NOTHING`, tenant.Team(), owner)
	engineSQL(t, s, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,?,'team.admin',1,?,?,?) ON CONFLICT DO NOTHING`, session, owner, time.Now().Add(time.Hour).UnixNano(), tenant.Team(), owner)
	engineSQL(t, s, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES (?,?,0) ON CONFLICT DO NOTHING`, session, tenant.Team())
	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: session})
	if err != nil {
		t.Fatal(err)
	}
	route, ok := capability.LookupRoute("POST", "/api/v1/team/billing/checkout")
	if !ok {
		t.Fatal("checkout route is missing")
	}
	a, err := capability.Authorize(t.Context(), s.AsOperator(), route, capability.Authenticated{Principal: p, Method: "POST", Pattern: "/api/v1/team/billing/checkout", Live: []capability.LiveAccess{{RuleIndex: 0}}}, capability.Target{Team: string(tenant.Team())})
	if err != nil {
		t.Fatal(err)
	}
	page, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, cents, provider)
	if err != nil {
		t.Fatal(err)
	}
	page.ProviderID, page.URL = provider, "https://checkout.stripe.com/prepay"
	if err := tenant.CompleteAuthorizedBillingPage(t.Context(), a, page); err != nil {
		t.Fatal(err)
	}
	return page.ReservationID
}

// PaidCreditsFixture settles a signed checkout against its committed reservation.
func PaidCreditsFixture(t *testing.T, s *store.Store, tenant *store.Tenant, cents int64) string {
	t.Helper()
	engineSQL(t, s, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	reservation := billingModelCheckout(t, s, tenant, "credit-fixture", cents)
	_, webhook := billingModelPaymentHandler(t, newEngineFixture(s, EngineController{}))
	webhook("paid-fixture", "checkout.session.completed", fmt.Sprintf(`{"id":"credit-fixture","object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":"pi_fixture","amount_total":%d,"currency":"usd"}`, cents))
	return "payment:test:" + reservation
}
