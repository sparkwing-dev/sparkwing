package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func teamHandle(t *testing.T, s *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	ctx := context.Background()
	if err := s.AsOperator().CreateTeam(ctx, team); err != nil {
		t.Fatalf("register %s: %v", team, err)
	}
	tenant, err := s.ForTeam(ctx, team)
	if err != nil {
		t.Fatalf("handle for %s: %v", team, err)
	}
	return tenant
}

// safety: the node is created through the unscoped surface because the nodes
// family is not ported yet; the ledger reads the team off the run, which is.
func readyTeamNode(t *testing.T, s *store.Store, tenant *store.Tenant, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := tenant.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run for %s: %v", tenant.Team(), err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := s.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
}

func teamExhaustedAt(t *testing.T, s *store.Store, team store.Team) int64 {
	t.Helper()
	var at int64
	if err := s.DB().QueryRow(storetest.Rebind(s,
		`SELECT credit_exhausted_at FROM teams WHERE name = ?`), string(team)).Scan(&at); err != nil {
		t.Fatalf("read %s's exhaustion stamp: %v", team, err)
	}
	return at
}

// A team that has spent its grants must stop only its own work. The fleet
// balance and one team's balance were the same number under a controller per
// team; on a shared controller summing them lets one customer's billing event
// refuse every other customer's claims.
// safety: each team's cloud runner holds a credential of that team, because
// a metered token is one team's like any other and claims nothing outside it.
func meteredTeamClaimant(t *testing.T, tenant *store.Tenant, principal string) store.ClaimIdentity {
	t.Helper()
	_, tok, err := tenant.CreateTokenWith(context.Background(), principal, store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now(), store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("mint %s's metered token: %v", tenant.Team(), err)
	}
	return store.ClaimIdentity{Principal: principal, TokenPrefix: tok.Prefix}
}

func TestCreditsOneTeamsEmptyBalanceRefusesOnlyItsOwnNodes(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	acmeRunner := meteredTeamClaimant(t, acme, "agent:acme-cloud")
	globexRunner := meteredTeamClaimant(t, globex, "agent:globex-cloud")

	floor := unpinnedNodeRateMicro * store.MinBillableSeconds
	if _, err := acme.GrantCredits(ctx, store.CreditGrantPaid, floor, "pay_acme", "admin"); err != nil {
		t.Fatalf("grant acme one claim: %v", err)
	}
	funded := int64(100 * store.MicroCreditsPerCent)
	if _, err := globex.GrantCredits(ctx, store.CreditGrantPaid, funded, "pay_globex", "admin"); err != nil {
		t.Fatalf("fund globex: %v", err)
	}

	readyTeamNode(t, s, acme, "run-acme-1", "build")
	if n, err := s.ClaimNextReadyNode(ctx, acmeRunner, "pod-a1", time.Minute, nil); err != nil || n == nil {
		t.Fatalf("acme's own grant must pay for its first claim: %v", err)
	}

	readyTeamNode(t, s, globex, "run-globex-1", "build")
	if n, err := s.ClaimNextReadyNode(ctx, globexRunner, "pod-g1", time.Minute, nil); err != nil || n == nil {
		t.Fatalf("globex must keep claiming while acme is empty: %v", err)
	}

	readyTeamNode(t, s, acme, "run-acme-2", "build")
	_, err := s.ClaimNextReadyNode(ctx, acmeRunner, "pod-a2", time.Minute, nil)
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("acme's second claim = %v, want ErrInsufficientCredits; "+
			"globex's grant must not pay for acme", err)
	}

	got, err := acme.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("acme balance: %v", err)
	}
	if got != 0 {
		t.Errorf("acme balance = %d, want 0; it holds only what it was granted", got)
	}
	got, err = globex.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("globex balance: %v", err)
	}
	if want := funded - floor; got != want {
		t.Errorf("globex balance = %d, want %d; acme's spend must not touch it", got, want)
	}
}

// A funded neighbor must not hide that a team has run out: the charge path
// reads the team's own balance, stamps the team's own row, and cancels only
// that team's node once its grace period elapses.
func TestCreditsFundedTeamDoesNotMaskAnotherTeamsExhaustion(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	acmeRunner := meteredTeamClaimant(t, acme, "agent:acme-cloud")
	globexRunner := meteredTeamClaimant(t, globex, "agent:globex-cloud")

	grace := int64(30)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
		t.Fatalf("set grace: %v", err)
	}
	floor := unpinnedNodeRateMicro * store.MinBillableSeconds
	if _, err := acme.GrantCredits(ctx, store.CreditGrantFree, floor, "", "admin"); err != nil {
		t.Fatalf("grant acme one claim: %v", err)
	}
	if _, err := globex.GrantCredits(ctx, store.CreditGrantPaid,
		100*store.MicroCreditsPerCent, "pay_globex", "admin"); err != nil {
		t.Fatalf("fund globex: %v", err)
	}

	readyTeamNode(t, s, acme, "run-acme", "build")
	if _, err := s.ClaimNextReadyNode(ctx, acmeRunner, "pod-a", time.Minute, nil); err != nil {
		t.Fatalf("acme claim: %v", err)
	}
	readyTeamNode(t, s, globex, "run-globex", "build")
	if _, err := s.ClaimNextReadyNode(ctx, globexRunner, "pod-g", time.Minute, nil); err != nil {
		t.Fatalf("globex claim: %v", err)
	}

	start := time.Now()
	rewindChargeWindow(t, s, "run-acme", "build", start)
	res := chargeAt(t, s, "run-acme", acmeRunner, start.Add(5*time.Second))
	if res.BalanceMicro > 0 {
		t.Fatalf("acme's balance = %d, want it spent", res.BalanceMicro)
	}
	if res.Cancel {
		t.Fatal("acme's grace period had not elapsed yet")
	}
	rewindChargeWindow(t, s, "run-acme", "build", start.Add(5*time.Second))
	if res := chargeAt(t, s, "run-acme", acmeRunner, start.Add(40*time.Second)); !res.Cancel {
		t.Fatalf("acme's node = %+v, want a cancellation; globex's balance must not mask it", res)
	}

	rewindChargeWindow(t, s, "run-globex", "build", start)
	res = chargeAt(t, s, "run-globex", globexRunner, start.Add(40*time.Second))
	if res.Cancel {
		t.Fatal("globex's node was cancelled for acme's empty balance")
	}
	if res.BalanceMicro <= 0 {
		t.Fatalf("globex's balance = %d, want it still funded", res.BalanceMicro)
	}

	if at := teamExhaustedAt(t, s, "acme"); at == 0 {
		t.Error("acme's row carries no exhaustion stamp")
	}
	if at := teamExhaustedAt(t, s, "globex"); at != 0 {
		t.Errorf("globex's row was stamped exhausted at %d by acme running out", at)
	}
}

// A local install holds exactly one team and never names it. Every number the
// unscoped surface reports is the one the default team's handle reports, so a
// self-hosted controller behaves after this change as it did before it.
func TestCreditsLocalInstallReadsOneTeamWithoutNamingIt(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	teams, err := s.AsOperator().ListTeams(ctx)
	if err != nil {
		t.Fatalf("list teams: %v", err)
	}
	if len(teams) != 1 || teams[0] != store.DefaultTeam {
		t.Fatalf("a fresh install lists teams %v, want only %q", teams, store.DefaultTeam)
	}

	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-local", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		100*store.MicroCreditsPerCent, "pay_local", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	floor := unpinnedNodeRateMicro * store.MinBillableSeconds
	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if want := int64(100*store.MicroCreditsPerCent) - floor; balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}

	fleet, err := s.CreditState(ctx, time.Hour)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	def, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatalf("default handle: %v", err)
	}
	scoped, err := def.CreditState(ctx, time.Hour)
	if err != nil {
		t.Fatalf("scoped state: %v", err)
	}
	if fleet.BalanceMicro != scoped.BalanceMicro ||
		fleet.GrantedMicro != scoped.GrantedMicro ||
		fleet.ChargedMicro != scoped.ChargedMicro {
		t.Fatalf("unscoped state %+v differs from the default team's %+v", fleet, scoped)
	}
	if fleet.BalanceMicro != balance {
		t.Fatalf("state balance = %d, want the ledger's %d", fleet.BalanceMicro, balance)
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
	if _, err := acme.GrantCredits(ctx, store.CreditGrantPaid, amount, "pay_shared", "admin"); err != nil {
		t.Fatalf("acme grant: %v", err)
	}
	if _, err := globex.GrantCredits(ctx, store.CreditGrantPaid, amount, "pay_shared", "admin"); !errors.Is(
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

// A refusal names the node it was refused for. The other team's node is the
// oldest waiting, so a refusal that named the queue's head would put one
// team's billing event on another team's run.
func TestCreditRefusalNamesTheRefusedNode(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	readyTeamNode(t, s, globex, "run-globex", "build")
	readyTeamNode(t, s, acme, "run-acme", "build")
	acmeRunner := meteredTeamClaimant(t, acme, "agent:acme-cloud")

	_, err := s.ClaimNextReadyNode(ctx, acmeRunner, "pod-a", time.Minute, nil)
	var shortfall *store.InsufficientCreditsError
	if !errors.As(err, &shortfall) {
		t.Fatalf("acme's claim on an empty balance = %v, want InsufficientCreditsError", err)
	}
	if shortfall.RunID != "run-acme" || shortfall.NodeID != "build" {
		t.Fatalf("refusal names %s/%s, want the refused run-acme/build", shortfall.RunID, shortfall.NodeID)
	}
}
