package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type standingRequestFixtureState struct {
	s       *Store
	tenant  *Tenant
	console *Console
	op      capability.Authorized
	owner   capability.Authorized
}

func standingRequestFixture(t *testing.T) standingRequestFixtureState {
	t.Helper()
	s, c, op := consoleFixture(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	op.Target.Team = "a"
	return standingRequestFixtureState{s: s, tenant: &Tenant{s: s, team: "a"}, console: c, op: op, owner: teamAuthorization(t, s, owner, "a", "POST", "/api/v1/team/billing/standing-request")}
}

func (f standingRequestFixtureState) request(t *testing.T, message string) StandingRequest {
	t.Helper()
	req, err := f.tenant.RequestAuthorizedStanding(t.Context(), f.owner, "High", message)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func (f standingRequestFixtureState) state(t *testing.T, id string) string {
	t.Helper()
	var state string
	if err := f.s.db.QueryRow(`SELECT state FROM standing_requests WHERE id=?`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestStandingRequestOneOpenPerTeamAndDecline(t *testing.T) {
	f := standingRequestFixture(t)
	ctx := t.Context()
	req := f.request(t, "  We run nightly fuzzing.  ")
	if req.State != StandingRequestOpen || req.Message != "We run nightly fuzzing." || req.RequestedBy != "owner" || req.RequestedAt.IsZero() {
		t.Fatalf("request=%+v", req)
	}
	if _, err := f.tenant.RequestAuthorizedStanding(ctx, f.owner, "High", "again"); !errors.Is(err, ErrStandingRequestOpen) {
		t.Fatalf("second open request: %v", err)
	}
	open, err := f.s.OpenStandingRequests(ctx, 50)
	if err != nil || len(open) != 1 || open[0].ID != req.ID || open[0].RequesterEmail != "owner@example.test" || open[0].Team != "a" {
		t.Fatalf("queue=%+v: %v", open, err)
	}
	if _, err := f.console.ConsoleDeclineStandingRequest(ctx, f.op, req.ID, " "); err == nil {
		t.Fatal("decline without a reason committed")
	}
	if _, err := f.console.ConsoleDeclineStandingRequest(ctx, f.op, req.ID, "Pay for 30 days first."); err != nil {
		t.Fatal(err)
	}
	if _, err := f.console.ConsoleDeclineStandingRequest(ctx, f.op, req.ID, "twice"); !errors.Is(err, ErrStandingRequestClosed) {
		t.Fatalf("retried decline: %v", err)
	}
	latest, found, err := f.tenant.LatestStandingRequest(ctx)
	if err != nil || !found || latest.State != StandingRequestDeclined || latest.DecidedBy != "operator" || latest.DecisionReason != "Pay for 30 days first." || latest.DecidedAt.IsZero() {
		t.Fatalf("latest=%+v found=%v: %v", latest, found, err)
	}
	f.request(t, "after decline")
	standingRequestEvents(t, f.s, map[string]int{BusinessEventStandingRequested: 2, BusinessEventStandingRequestDecided: 1})
}

func TestStandingRequestDecisionsResolveOnlyTheReviewedRequest(t *testing.T) {
	f := standingRequestFixture(t)
	ctx := t.Context()
	reviewed := f.request(t, "first")
	if _, err := f.console.ConsoleDeclineStandingRequest(ctx, f.op, reviewed.ID, "not yet"); err != nil {
		t.Fatal(err)
	}
	successor := f.request(t, "second")
	if _, err := f.console.ConsoleDeclineStandingRequest(ctx, f.op, reviewed.ID, "stale"); !errors.Is(err, ErrStandingRequestClosed) {
		t.Fatalf("stale decline: %v", err)
	}
	if _, err := f.console.ConsoleSetBillingTrust(ctx, f.op, BillingTrustChange{Trust: BillingTrustGranted, Reason: "stale", Request: reviewed.ID}); !errors.Is(err, ErrStandingRequestClosed) {
		t.Fatalf("stale approval: %v", err)
	}
	if b := billingStandingOf(t, f.tenant); b.Trust != BillingTrustAutomatic || f.state(t, successor.ID) != StandingRequestOpen {
		t.Fatalf("stale decision changed trust=%s successor=%s", b.Trust, f.state(t, successor.ID))
	}
	if _, err := f.console.ConsoleSetBillingTrust(ctx, f.op, BillingTrustChange{Trust: BillingTrustRevoked, Reason: "x", Request: successor.ID}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("revocation approved a request: %v", err)
	}
	if _, err := f.console.ConsoleSetBillingTrust(ctx, f.op, BillingTrustChange{Trust: BillingTrustGranted, Reason: "Known customer", Request: successor.ID}); err != nil {
		t.Fatal(err)
	}
	if b := billingStandingOf(t, f.tenant); b.Tier != "High" || f.state(t, successor.ID) != StandingRequestApproved {
		t.Fatalf("approval: tier=%s state=%s", b.Tier, f.state(t, successor.ID))
	}
	if _, err := f.tenant.RequestAuthorizedStanding(ctx, f.owner, "High", "more"); err == nil || !strings.Contains(err.Error(), "already has High standing") {
		t.Fatalf("request at High: %v", err)
	}
	standingRequestEvents(t, f.s, map[string]int{BusinessEventStandingRequested: 2, BusinessEventStandingRequestDecided: 2, BusinessEventBillingTrustChanged: 1})
}

func TestStandingGrantWithoutRequestLeavesRequestsOpen(t *testing.T) {
	f := standingRequestFixture(t)
	req := f.request(t, "pending")
	if _, err := f.console.ConsoleSetBillingTrust(t.Context(), f.op, BillingTrustChange{Trust: BillingTrustGranted, Reason: "from the team view"}); err != nil {
		t.Fatal(err)
	}
	if f.state(t, req.ID) != StandingRequestOpen {
		t.Fatal("a grant naming no request decided one")
	}
}

func TestConsoleDeclineNeedsTheCurrentOperator(t *testing.T) {
	f := standingRequestFixture(t)
	req := f.request(t, "pending")
	forged := f.op
	forged.Principal.Class = capability.CredentialSession
	f.console.allowed = func(string) bool { return false }
	for _, a := range []capability.Authorized{f.op, forged} {
		_, err := f.console.ConsoleDeclineStandingRequest(t.Context(), a, req.ID, "no")
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.Forbidden || f.state(t, req.ID) != StandingRequestOpen {
			t.Fatalf("unauthorized decline: %v state=%s", err, f.state(t, req.ID))
		}
	}
	f.console.allowed = func(id string) bool { return id == "operator" }
	if _, err := f.console.ConsoleDeclineStandingRequest(t.Context(), f.op, req.ID, "no"); err != nil || f.state(t, req.ID) != StandingRequestDeclined {
		t.Fatalf("operator decline: %v", err)
	}
}

func TestStandingRequestRefusesInvalidInputAndLostAuthority(t *testing.T) {
	f := standingRequestFixture(t)
	for _, in := range [][2]string{{"Trusted", "m"}, {"Ultra", "m"}, {"High", "   "}, {"High", strings.Repeat("é", 1001)}} {
		if _, err := f.tenant.RequestAuthorizedStanding(t.Context(), f.owner, in[0], in[1]); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q: %v", in, err)
		}
	}
	if _, err := f.tenant.RequestAuthorizedStanding(t.Context(), f.owner, "High", strings.Repeat("é", 1000)); err != nil {
		t.Fatalf("1,000 characters refused: %v", err)
	}
	rewriteSchemaExec(t, f.s.db, `DELETE FROM standing_requests`)
	rewriteSchemaExec(t, f.s.db, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='owner'`)
	if _, err := f.tenant.RequestAuthorizedStanding(t.Context(), f.owner, "High", "demoted"); err == nil {
		t.Fatal("a demoted owner's request committed")
	}
	var n int
	if err := f.s.db.QueryRow(`SELECT COUNT(*) FROM standing_requests`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("requests after refusal=%d: %v", n, err)
	}
}

func billingStandingOf(t *testing.T, tenant *Tenant) BillingStanding {
	t.Helper()
	b, err := tenant.BillingStanding(t.Context(), tenant.s.now())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func standingRequestEvents(t *testing.T, s *Store, want map[string]int) {
	t.Helper()
	for kind, n := range want {
		var got int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM business_events WHERE team='a' AND kind=?`, kind).Scan(&got); err != nil || got != n {
			t.Fatalf("%s events=%d want %d: %v", kind, got, n, err)
		}
	}
}
