package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func standingRequestFixture(t *testing.T) (*Store, *Tenant, *Console, capability.Authorized) {
	t.Helper()
	s, c, a := consoleFixture(t)
	authorityMember(t, s, "owner", RoleOwner)
	a.Target.Team = "a"
	return s, &Tenant{s: s, team: "a"}, c, a
}

func TestStandingRequestOneOpenPerTeamAndDecline(t *testing.T) {
	s, tenant, c, a := standingRequestFixture(t)
	ctx := t.Context()
	decline := func(reason string) error {
		_, err := c.ConsoleDeclineStandingRequest(ctx, a, reason)
		return err
	}
	req, err := tenant.RequestStanding(ctx, StandingRequestInput{Tier: "High", Message: "  We run nightly fuzzing.  ", Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if req.State != StandingRequestOpen || req.Message != "We run nightly fuzzing." || req.RequestedBy != "owner" || req.RequestedAt.IsZero() {
		t.Fatalf("request=%+v", req)
	}
	if _, err := tenant.RequestStanding(ctx, StandingRequestInput{Tier: "High", Message: "again", Actor: "owner"}); !errors.Is(err, ErrStandingRequestOpen) {
		t.Fatalf("second open request: %v", err)
	}
	open, err := s.OpenStandingRequests(ctx, 50)
	if err != nil || len(open) != 1 || open[0].ID != req.ID || open[0].RequesterEmail != "owner@example.test" || open[0].Team != "a" {
		t.Fatalf("queue=%+v: %v", open, err)
	}
	if err := decline(" "); err == nil {
		t.Fatal("decline without a reason committed")
	}
	if err := decline("Pay for 30 days first."); err != nil {
		t.Fatal(err)
	}
	if err := decline("twice"); err == nil {
		t.Fatal("declined a request that is no longer open")
	}
	latest, found, err := tenant.LatestStandingRequest(ctx)
	if err != nil || !found || latest.State != StandingRequestDeclined || latest.DecidedBy != "operator" || latest.DecisionReason != "Pay for 30 days first." || latest.DecidedAt.IsZero() {
		t.Fatalf("latest=%+v found=%v: %v", latest, found, err)
	}
	if open, err := s.OpenStandingRequests(ctx, 50); err != nil || len(open) != 0 {
		t.Fatalf("declined request stayed queued: %+v %v", open, err)
	}
	if _, err := tenant.RequestStanding(ctx, StandingRequestInput{Tier: "High", Message: "after decline", Actor: "owner"}); err != nil {
		t.Fatalf("request after decline: %v", err)
	}
	standingRequestEvents(t, s, map[string]int{BusinessEventStandingRequested: 2, BusinessEventStandingRequestDecided: 1})
}

func TestStandingRequestApprovedByTheStandingGrant(t *testing.T) {
	s, tenant, c, auth := standingRequestFixture(t)
	ctx := t.Context()
	req, err := tenant.RequestStanding(ctx, StandingRequestInput{Tier: "High", Message: "Release builds need 256 vCPU.", Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConsoleSetBillingTrust(ctx, auth, BillingTrustChange{Trust: BillingTrustGranted, Reason: "Known customer"}); err != nil {
		t.Fatal(err)
	}
	latest, found, err := tenant.LatestStandingRequest(ctx)
	if err != nil || !found || latest.ID != req.ID || latest.State != StandingRequestApproved || latest.DecidedBy != "operator" || latest.DecisionReason != "Known customer" {
		t.Fatalf("latest=%+v: %v", latest, err)
	}
	if b := billingStandingOf(t, tenant); b.Tier != "High" {
		t.Fatalf("approved standing=%s", b.Tier)
	}
	if _, err := tenant.RequestStanding(ctx, StandingRequestInput{Tier: "High", Message: "more", Actor: "owner"}); err == nil || !strings.Contains(err.Error(), "already has High standing") {
		t.Fatalf("request at High: %v", err)
	}
	standingRequestEvents(t, s, map[string]int{BusinessEventStandingRequested: 1, BusinessEventStandingRequestDecided: 1, BusinessEventBillingTrustChanged: 1})
}

func TestStandingRequestRefusesInvalidInput(t *testing.T) {
	_, tenant, _, _ := standingRequestFixture(t)
	for _, in := range []StandingRequestInput{
		{Tier: "Trusted", Message: "m", Actor: "owner"},
		{Tier: "Ultra", Message: "m", Actor: "owner"},
		{Tier: "High", Message: "   ", Actor: "owner"},
		{Tier: "High", Message: strings.Repeat("x", 1001), Actor: "owner"},
		{Tier: "High", Message: "m"},
	} {
		if _, err := tenant.RequestStanding(t.Context(), in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	if _, found, err := tenant.LatestStandingRequest(t.Context()); err != nil || found {
		t.Fatalf("refused input left a request: %v %v", found, err)
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
