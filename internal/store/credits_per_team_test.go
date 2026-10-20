package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func teamHandle(t *testing.T, s *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	ctx := context.Background()
	if err := s.AsOperator().TestOnlyCreateTeam(ctx, team); err != nil {
		t.Fatalf("register %s: %v", team, err)
	}
	tenant, err := s.ForTeam(ctx, team)
	if err != nil {
		t.Fatalf("handle for %s: %v", team, err)
	}
	return tenant
}

func TestCreditsDefaultTenantReadsItsLedger(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	teams, err := s.AsOperator().ListTeams(ctx)
	if err != nil {
		t.Fatalf("list teams: %v", err)
	}
	if len(teams) != 1 || teams[0] != store.DefaultTeam {
		t.Fatalf("a fresh install lists teams %v, want only %q", teams, store.DefaultTeam)
	}

	def := tenant(t, s, store.DefaultTeam)
	if _, err := def.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "pay_local", CreatedBy: "admin"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	floor := unpinnedNodeRateMicro * store.SliceSeconds
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('local',?,'run-local','build','award','usage',?,?,?)`), store.DefaultTeam, store.SliceSeconds, floor, time.Now().UnixNano()); err != nil {
		t.Fatalf("seed debit: %v", err)
	}
	balance, err := def.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if want := int64(100*store.MicroCreditsPerCent) - floor; balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}

	scoped, err := def.CreditState(ctx, time.Hour)
	if err != nil {
		t.Fatalf("scoped state: %v", err)
	}
	if scoped.BalanceMicro != balance {
		t.Fatalf("state balance = %d, want the ledger's %d", scoped.BalanceMicro, balance)
	}
}

// A reference is a payment id, so it is the deployment's idempotency key. A
// second team replaying it must be refused rather than granted the first
// team's credits.
func TestCreditsGrantReferenceCannotCrossTeams(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")

	amount := int64(10 * store.MicroCreditsPerCent)
	if _, err := acme.TestOnlySettlePaidCredit(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: amount, Reference: "pay_shared", CreatedBy: "admin"}); err != nil {
		t.Fatalf("acme grant: %v", err)
	}
	if _, err := globex.TestOnlySettlePaidCredit(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: amount, Reference: "pay_shared", CreatedBy: "admin"}); !errors.Is(
		err, store.ErrCreditGrantConflict) {
		t.Fatalf("globex replaying acme's payment = %v, want ErrCreditGrantConflict", err)
	}
	got, err := globex.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("globex balance: %v", err)
	}
	if got != 0 {
		t.Fatalf("globex balance = %d, want 0", got)
	}
}
