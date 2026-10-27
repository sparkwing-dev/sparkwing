package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

type runBlocker struct {
	t    *testing.T
	ctx  context.Context
	db   *sql.DB
	tx   *sql.Tx
	pid  int
	done chan error
}

func blockRun(t *testing.T, ctx context.Context, st *store.Store) *runBlocker {
	t.Helper()
	b := &runBlocker{t: t, ctx: ctx, db: st.DB(), done: make(chan error, 2)}
	var err error
	if b.tx, err = b.db.BeginTx(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.tx.Rollback() })
	if _, err := b.tx.ExecContext(ctx, `SELECT id FROM runs WHERE team='a' AND id='run' FOR NO KEY UPDATE`); err != nil {
		t.Fatal(err)
	}
	if err := b.tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&b.pid); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *runBlocker) start(call func() error) { go func() { b.done <- call() }() }

func (b *runBlocker) waitBlocked(want int) string {
	b.t.Helper()
	for {
		select {
		case err := <-b.done:
			b.t.Fatalf("store call finished while the run was held: %v", err)
		default:
		}
		var pids string
		var count int
		err := b.db.QueryRowContext(b.ctx, `WITH RECURSIVE blocked AS (
   SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
   UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked w ON w.pid=ANY(pg_blocking_pids(a.pid))
  ) SELECT COUNT(*),COALESCE(string_agg(pid::text,','),'') FROM blocked`, b.pid).Scan(&count, &pids)
		if err != nil {
			b.t.Fatal(err)
		}
		if count >= want {
			return pids
		}
		runtime.Gosched()
	}
}

func (b *runBlocker) held(query string) bool {
	b.t.Helper()
	_, err := b.db.ExecContext(b.ctx, query+" NOWAIT")
	if err != nil && !strings.Contains(err.Error(), "could not obtain lock") {
		b.t.Fatal(err)
	}
	return err != nil
}

func (b *runBlocker) release(calls int) {
	b.t.Helper()
	if err := b.tx.Rollback(); err != nil {
		b.t.Fatal(err)
	}
	for range calls {
		if err := <-b.done; err != nil {
			b.t.Fatal(err)
		}
	}
}

func lockOrderStore(t *testing.T) (*store.Store, store.UnitDemand, context.Context) {
	st := storetest.NewPostgres(t).Open(t)
	demand := st.TestOnlyUnitDemand(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	return st, demand, ctx
}

func TestPostgresClaimLocksDelegatedClaimantRootBeforeRun(t *testing.T) {
	st, demand, ctx := lockOrderStore(t)
	st.TestOnlyDelegatedClaimant(t, "delegate")
	demand.CapabilityPrefix = "delegate"
	b := blockRun(t, ctx, st)
	b.start(func() error { _, err := st.AsOperator().ClaimUnit(ctx, demand); return err })
	b.waitBlocked(1)
	for _, prefix := range []string{"runner", "delegate"} {
		if !b.held(`SELECT 1 FROM capabilities WHERE prefix='` + prefix + `' FOR SHARE`) {
			t.Fatalf("claim waits for the run without write strength on %s", prefix)
		}
	}
	b.release(1)
}

func TestPostgresTeamDeletionWaitsForClaimBeforeLedger(t *testing.T) {
	postgresTeamMutationWaitsForClaimBeforeLedger(t, false)
}

func TestPostgresBillingWaitsForClaimBeforeLedger(t *testing.T) {
	postgresTeamMutationWaitsForClaimBeforeLedger(t, true)
}

func postgresTeamMutationWaitsForClaimBeforeLedger(t *testing.T, billing bool) {
	t.Helper()
	st := storetest.NewPostgres(t).Open(t)
	st.DB().SetMaxOpenConns(8)
	f := st.TestOnlyRankingFixture(t, true)
	if err := f.Rank(t, f.Admin); err != nil {
		t.Fatal(err)
	}
	f.At(time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if _, err := st.AsOperator().ConfigureDeployment(ctx, capability.PaymentTest, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE capabilities SET authority_json=convert_to(jsonb_set(convert_from(authority_json,'UTF8')::jsonb,'{root_metered}','true')::text,'UTF8') WHERE prefix=$1`, f.Self); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','owner','owner',1)`); err != nil {
		t.Fatal(err)
	}
	principal, err := st.AsOperator().ResolveAuthority(ctx, f.Owner.Authority)
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: principal, Target: capability.Target{Team: "a"}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	tenant, err := st.ForTeam(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	b := blockRun(t, ctx, st)
	b.start(func() error {
		_, err := st.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Self, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}})
		return err
	})
	b.waitBlocked(1)
	b.start(func() error {
		if billing {
			_, err := tenant.PrepareAuthorizedBillingPage(ctx, a, capability.BillingPageCredits, 2000, "claim-contention")
			return err
		}
		_, err := tenant.DeleteAuthorizedTeam(ctx, a)
		return err
	})
	b.waitBlocked(2)
	var free bool
	if err := b.tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('sparkwing/ledger/a'))`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if !free {
		t.Fatal("team mutation holds the ledger while waiting for the claim's team lock")
	}
	b.release(2)
}

func TestPostgresRenewalLocksObservedAuthorityBeforeRun(t *testing.T) {
	st, demand, ctx := lockOrderStore(t)
	claim, err := st.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		t.Fatal(err)
	}
	b := blockRun(t, ctx, st)
	b.start(func() error {
		_, err := st.AsOperator().RenewUnit(ctx, claim.Award.CapabilityPrefix, false)
		return err
	})
	b.waitBlocked(1)
	for _, prefix := range []string{claim.Award.CapabilityPrefix, "runner"} {
		if !b.held(`SELECT 1 FROM capabilities WHERE prefix='` + prefix + `' FOR SHARE`) {
			t.Fatalf("renewal waits for the run without write strength on %s", prefix)
		}
	}
	b.release(1)
}

func TestPostgresLauncherPreparationLocksExchangedAuthorityBeforeRun(t *testing.T) {
	st, demand, ctx := lockOrderStore(t)
	demand.CapabilityPrefix = st.TestOnlyLauncher(t)
	claim, err := st.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		t.Fatal(err)
	}
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	bootstrap, err := st.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	fence := capability.LeaseRef{Kind: bootstrap.Capability.Unit, ID: bootstrap.Capability.UnitID, Holder: claim.Award.Holder, Generation: int64(claim.Award.Generation)}
	exchanged, err := st.AsOperator().ExchangeCapability(ctx, capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	b := blockRun(t, ctx, st)
	b.start(func() error {
		_, _, err := st.AsOperator().ReconcileLauncherAwards(ctx, root, nil, nil, []capability.LeaseRef{fence}, nil, nil)
		return err
	})
	b.waitBlocked(1)
	if !b.held(`SELECT 1 FROM capabilities WHERE prefix='` + exchanged.Capability.Prefix + `' FOR NO KEY UPDATE`) {
		t.Fatal("preparation waits for the run without locking its current exchanged authority")
	}
	b.start(func() error { _, err := st.AsOperator().RenewUnit(ctx, exchanged.Capability.Prefix, false); return err })
	b.waitBlocked(2)
	b.release(2)
}

func TestPostgresLauncherPreparationRestartsDiscoveryAfterConcurrentExchange(t *testing.T) {
	st, demand, ctx := lockOrderStore(t)
	demand.CapabilityPrefix = st.TestOnlyLauncher(t)
	claim, err := st.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		t.Fatal(err)
	}
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	bootstrap, err := st.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	fence := capability.LeaseRef{Kind: bootstrap.Capability.Unit, ID: bootstrap.Capability.UnitID, Holder: claim.Award.Holder, Generation: int64(claim.Award.Generation)}
	b := blockRun(t, ctx, st)
	b.start(func() error {
		_, err := st.AsOperator().ExchangeCapability(ctx, capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
		return err
	})
	b.waitBlocked(1)
	b.start(func() error {
		_, prepared, err := st.AsOperator().ReconcileLauncherAwards(ctx, root, nil, nil, []capability.LeaseRef{fence}, nil, nil)
		if err == nil && len(prepared) != 0 {
			return fmt.Errorf("preparation retained exchanged authority: %+v", prepared)
		}
		return err
	})
	b.waitBlocked(2)
	b.release(2)
}

func TestPostgresExchangeLocksBootstrapAndRunBeforeJob(t *testing.T) {
	st, demand, ctx := lockOrderStore(t)
	demand.CapabilityPrefix = st.TestOnlyLauncher(t)
	claim, err := st.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := claim.Award.CapabilityPrefix
	b := blockRun(t, ctx, st)
	b.start(func() error {
		_, err := st.AsOperator().ExchangeCapability(ctx, capability.ExchangeSpec{BootstrapPrefix: bootstrap})
		return err
	})
	b.waitBlocked(1)
	if !b.held(`SELECT 1 FROM capabilities WHERE prefix='` + bootstrap + `' FOR SHARE`) {
		t.Fatal("exchange waits for the run without write strength on its bootstrap")
	}
	if b.held(`SELECT 1 FROM nodes WHERE team='a' AND run_id='run' AND node_id='job' FOR UPDATE`) {
		t.Fatal("exchange holds the job while waiting for its run")
	}
	b.release(1)
}

func TestPostgresConcurrentEvaluationReleasesRequestTheRunForWriting(t *testing.T) {
	st, demand, ctx := lockOrderStore(t)
	demand.CapabilityPrefix = st.TestOnlyLauncher(t)
	st.TestOnlyEvaluations(t, "job", "second")
	var claims []store.UnitClaim
	for _, job := range []string{"job", "second"} {
		demand.Target = &store.UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: runspec.NodeID(job), SourceGeneration: 7}
		claim, err := st.AsOperator().ClaimUnit(ctx, demand)
		if err != nil {
			t.Fatal(err)
		}
		claims = append(claims, claim)
	}
	b := blockRun(t, ctx, st)
	for _, claim := range claims {
		b.start(func() error { return st.ReleaseUnit(ctx, claim, claim.Award.CapabilityPrefix) })
	}
	pids := b.waitBlocked(2)
	if !b.held(`SELECT name FROM teams WHERE name='a' FOR KEY SHARE`) {
		t.Fatal("release waits for the run without locking its team for update")
	}
	// safety: a row-lock waiter's tuple lock records its requested strength; ExclusiveLock is FOR NO KEY UPDATE.
	var modes string
	if err := b.db.QueryRowContext(ctx, `SELECT COALESCE(string_agg(mode,',' ORDER BY mode),'') FROM pg_locks WHERE locktype='tuple' AND relation='runs'::regclass AND pid::text=ANY(string_to_array($1,','))`, pids).Scan(&modes); err != nil {
		t.Fatal(err)
	}
	// The second release waits for the first release's team lock.
	if modes != "ExclusiveLock" {
		t.Fatalf("releases wait for the run in modes %v", modes)
	}
	b.release(2)
}

func TestPostgresRenewalWaitsForMembershipBeforeLockingAuthority(t *testing.T) {
	st := storetest.NewPostgres(t).Open(t)
	f := st.TestOnlyRankingFixture(t, true)
	claim, err := f.Claim(t, f.Personal, capability.UnitNode)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	blocker := blockRun(t, ctx, st)
	if _, err := blocker.tx.ExecContext(ctx, `SELECT account_id FROM memberships WHERE team='a' AND account_id='owner' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	blocker.start(func() error {
		_, err := st.AsOperator().RenewUnit(ctx, claim.Award.CapabilityPrefix, false)
		return err
	})
	blocker.waitBlocked(1)
	for _, prefix := range []string{f.Personal, claim.Award.CapabilityPrefix} {
		if blocker.held(`SELECT 1 FROM capabilities WHERE prefix='` + prefix + `' FOR UPDATE`) {
			t.Fatalf("renewal holds %s while waiting for membership", prefix)
		}
	}
	blocker.release(1)
}
