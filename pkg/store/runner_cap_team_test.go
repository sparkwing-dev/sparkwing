package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestRunnerCapRequiresTeam(t *testing.T) {
	s := storetest.Open(t)
	if _, err := s.RunnerCapFor(context.Background(), "", time.Now()); !errors.Is(err, store.ErrNoTeam) {
		t.Fatalf("empty team = %v, want ErrNoTeam", err)
	}
}

func TestRunnerCapPaidGrantRaisesOnlyItsTeamsClaims(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	paid := teamHandle(t, s, "paid-team")
	free := teamHandle(t, s, "free-team")
	scaleTo(t, s, 1, 5000)
	if _, err := paid.GrantCredits(ctx, store.CreditGrantPaid, 5000*store.MicroCreditsPerCredit, "pay_paid", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := free.GrantCredits(ctx, store.CreditGrantFree, 5000*store.MicroCreditsPerCredit, "welcome", "admin"); err != nil {
		t.Fatal(err)
	}
	paidRunner := meteredTeamClaimant(t, paid, "agent:cloud")
	freeRunner := meteredTeamClaimant(t, free, "agent:cloud")
	for _, suffix := range []string{"1", "2"} {
		readyTeamNode(t, s, free, "free-"+suffix, "build")
		readyTeamNode(t, s, paid, "paid-"+suffix, "build")
	}
	if _, err := s.ClaimNextReadyNode(ctx, freeRunner, "free-pod-1", time.Minute, nil); err != nil {
		t.Fatalf("free team's first claim: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, freeRunner, "free-pod-2", time.Minute, nil); err == nil {
		t.Fatal("free team's second claim used the paid team's runner cap")
	} else {
		refusal := limitRefusal(t, err, store.ComputeLimitConcurrentRunners)
		if refusal.Cap != 1 {
			t.Fatalf("free team's cap = %d, want 1", refusal.Cap)
		}
	}
	for _, pod := range []string{"paid-pod-1", "paid-pod-2"} {
		if _, err := s.ClaimNextReadyNode(ctx, paidRunner, pod, time.Minute, nil); err != nil {
			t.Fatalf("paid team's %s claim: %v", pod, err)
		}
	}
}

func TestRunnerCapPaymentReversalRetiresOnlyItsTeamsCachedCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a := teamHandle(t, s, "team-a")
	b := teamHandle(t, s, "team-b")
	scaleTo(t, s, 1, 5000)
	for _, team := range []*store.Tenant{a, b} {
		if _, err := team.GrantCredits(ctx, store.CreditGrantPaid, 5000*store.MicroCreditsPerCredit,
			"pay_"+string(team.Team()), "admin"); err != nil {
			t.Fatal(err)
		}
	}
	for _, team := range []store.Team{a.Team(), b.Team()} {
		got, err := s.RunnerCapFor(ctx, team, time.Now())
		if err != nil || got.Cap != 2 {
			t.Fatalf("%s's initial cap = %+v, %v; want 2", team, got, err)
		}
	}
	if _, err := a.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -5000 * store.MicroCreditsPerCredit,
		Reference: "refund_a", Reverses: "pay_team-a", CreatedBy: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	for team, want := range map[store.Team]int64{a.Team(): 1, b.Team(): 2} {
		got, err := s.RunnerCapFor(ctx, team, time.Now())
		if err != nil || got.Cap != want {
			t.Fatalf("%s's cap after reversal = %+v, %v; want %d", team, got, err, want)
		}
	}
}

func TestRunnerCapReversalMatchesPaymentInsideItsTeam(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a := teamHandle(t, s, "team-a")
	b := teamHandle(t, s, "team-b")
	scaleTo(t, s, 1, 5000)
	for _, team := range []*store.Tenant{a, b} {
		if _, err := team.GrantCredits(ctx, store.CreditGrantPaid, 5000*store.MicroCreditsPerCredit,
			"pay_"+string(team.Team()), "admin"); err != nil {
			t.Fatal(err)
		}
	}
	// safety: a malformed imported reversal cannot take capacity from a different team's payment.
	_, err := s.DB().ExecContext(ctx, storetest.Rebind(s, `INSERT INTO credit_grants
	  (team, id, kind, amount_micro, reference, reverses, created_by, created_at)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), string(b.Team()), "wrong-team-refund",
		store.CreditGrantReversal, -5000*store.MicroCreditsPerCredit, "refund_wrong",
		"pay_team-a", "admin", time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.RunnerCapFor(ctx, b.Team(), time.Now())
	if err != nil || got.Cap != 2 {
		t.Fatalf("team B cap = %+v, %v; want its own paid step", got, err)
	}
}
