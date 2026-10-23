package store

import (
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func meteredUnitFixture(t *testing.T, cloud bool) (*Store, UnitDemand, *time.Time) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	root := authorityRoot("paid-runner", "a")
	root.RootMetered = true
	root.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	if cloud {
		root.Subject.Kind, root.Team = capability.SubjectService, ""
		root.Grants = []capability.Grant{capability.ClaimsLaunch}
	}
	authorityPersist(t, s, root)
	demand.CapabilityPrefix = root.Prefix
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('fund','a','paid',1000000,1)`)
	table := CreditRateTable{{Cores: 2, MicroPerSecond: 2}, {Cores: 4, MicroPerSecond: 10}}
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
		t.Fatal(err)
	}
	return s, demand, &now
}

func meteredUnitClaim(t *testing.T, s *Store, demand UnitDemand) (UnitClaim, runstate.Actor) {
	t.Helper()
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	if principal.Capability.Delivery == capability.DeliveryBootstrap {
		issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
		if err != nil {
			t.Fatal(err)
		}
		claim.Award.CapabilityPrefix = issued.Capability.Prefix
		principal.Capability = issued.Capability
	}
	_, actor, err := renewalActor(principal.Capability)
	if err != nil {
		t.Fatal(err)
	}
	return claim, actor
}

func unitBillingTotal(t *testing.T, s *Store, want int64, settled bool) {
	t.Helper()
	var amount int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(amount_micro),0) FROM credit_charges`).Scan(&amount); err != nil || amount != want {
		t.Fatalf("charges=%d, %v; want %d", amount, err, want)
	}
	var pending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM billing_awards WHERE settled_at IS NULL`).Scan(&pending); err != nil || (pending == 0) != settled {
		t.Fatalf("pending=%d, %v; settled=%t", pending, err, settled)
	}
}

func TestMeteredUnitClaimAndReservationCommitTogether(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, false)
	rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err == nil || len(claim.Bearer) != 0 {
		t.Fatalf("unfunded award: %+v, %v", claim, err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 0)
	rewriteSchemaCount(t, s.db, "credit_charges", 0)
	row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Generation != 0 || row.ClaimedBy != "" || row.StateSeq != 0 {
		t.Fatalf("failed reservation changed job: %+v, %v", row, err)
	}
}

func TestMeteredUnitConcurrentClaimsCannotSpendTheSameCredit(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, false)
	rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=600`)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,'other',status,ready_at,spec_json,execution_json FROM nodes WHERE node_id='job'`)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, node := range []string{"job", "other"} {
		group.Go(func() {
			request := demand
			request.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: runspec.NodeID(node)}
			_, err := s.AsOperator().ClaimUnit(t.Context(), request)
			results <- err
		})
	}
	group.Wait()
	close(results)
	var success int
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrInsufficientCredits) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("awards=%d; want one funded award", success)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
	unitBillingTotal(t, s, 600, false)
}

func TestMeteredUnitQueuePassesOverUnaffordableWork(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'b','older',number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,0,log_retention_class,log_retention_days FROM runs WHERE id='run'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('b','older','trigger',1,1,?,?,?)`, now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT 'b','older',node_id,status,ready_at,spec_json,execution_json FROM nodes WHERE run_id='run'`)
	demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.Team != "a" || claim.RunID != "run" {
		t.Fatalf("unfunded queue head blocked funded work: %+v, %v", claim, err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
	var claimed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE team='b' AND claimed_by IS NOT NULL`).Scan(&claimed); err != nil || claimed != 0 {
		t.Fatalf("unfunded team acquired work: %d, %v", claimed, err)
	}
}

func TestMeteredUnitEvalSettlementUsesPersistedLease(t *testing.T) {
	tenant, now, request := expansionFixture(t)
	s := tenant.s
	id, err := encodeAwardUnit(UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	c := authorityRoot("evaluation", "a")
	c.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}
	c.Issuance, c.Purpose = capability.Award, capability.PurposeEval
	c.Unit, c.UnitID, c.Delivery = capability.UnitEval, id, capability.DeliveryDirect
	c.Subject.ID = "eval-host"
	c.Grants = []capability.Grant{capability.ExpansionSubmit}
	c.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "eval-host", Generation: 2, Expansion: &capability.LeaseRef{Kind: capability.UnitEval, ID: id, Holder: "eval-host", Generation: 8}}
	c.Billing = &capability.BillingProvenance{PayerTeam: "a", Metered: true, ExecutorClass: "cloud", ReservationID: "eval-award", Start: capability.BillingAtStart, Unit: capability.BillingEval}
	authorityPersist(t, s, c)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('fund','a','paid',1000000,1)`)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return recordBillingAwardTx(t.Context(), tx, c, CreditRate{Cores: 1, MicroPerSecond: 2}, 0)
	}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE id='run'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','controller',4,?)`, now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,capability_prefix,lease_expires_at,owner_generation,claimed_at) VALUES ('a','run','job',2,'claimed',8,'eval-host','evaluation',?,4,1)`, now.Add(time.Minute).UnixNano())
	if _, err := s.AsOperator().RenewUnit(t.Context(), c.Prefix, true); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Second)
	request.Actor = runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerEval, EvalFence: runspec.EvalFence{RunID: request.Source.RunID, SourceNodeID: request.Source.NodeID, SourceGeneration: request.SourceGeneration, Holder: "eval-host", Generation: 8}}}
	request.Failure.Code = apierr.SDKError
	if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 22, true)
}

func TestMeteredUnitNodeLifecycleUsesImmutableAward(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		t.Run(map[bool]string{false: "runner", true: "cloud"}[cloud], func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, cloud)
			awarded := *now
			_, actor := meteredUnitClaim(t, s, demand)
			unitBillingTotal(t, s, 600, false)
			*now = now.Add(5 * time.Second)
			tenant := &Tenant{s: s, team: "a"}
			admitExecution(t, tenant, actor)
			started, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)})
			if err != nil {
				t.Fatal(err)
			}
			var began int64
			wantStart := awarded.UnixNano()
			if cloud {
				wantStart = now.UnixNano()
			}
			if err := s.db.QueryRow(`SELECT started_at FROM billable_intervals`).Scan(&began); err != nil || began != wantStart {
				t.Fatalf("compute start=%d, %v; want %d", began, err, wantStart)
			}
			table := CreditRateTable{{Cores: 2, MicroPerSecond: 200}, {Cores: 4, MicroPerSecond: 1000}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(10 * time.Second)
			finish := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(int64(started.Attempt))}
			for replay := range 2 {
				applied, err := tenant.TransitionNode(t.Context(), finish)
				if err != nil || applied.Replayed != (replay == 1) {
					t.Fatalf("finish: %+v, %v", applied, err)
				}
			}
			unitBillingTotal(t, s, 600, true)
			rewriteSchemaCount(t, s.db, "billable_intervals", 1)
		})
	}
}

func TestMeteredUnitUnstartedCloudReleaseRefundsReservation(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, true)
	claim, _ := meteredUnitClaim(t, s, demand)
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 0, true)
}

func TestMeteredUnitPlanReservationRefundsWithoutCompute(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, true)
	demand.Target = &UnitRef{Kind: capability.UnitPlan, RunID: "run"}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	claim, _ := meteredUnitClaim(t, s, demand)
	unitBillingTotal(t, s, 120, false)
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 0, true)
}

func TestMeteredUnitCloudRenewalStartsComputeWithoutAnAttempt(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	claim, _ := meteredUnitClaim(t, s, demand)
	*now = now.Add(20 * time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Status != runstate.StatusPending || row.Attempt != 0 || row.StartedAt != nil {
		t.Fatalf("renewal consumed an attempt: %+v, %v", row, err)
	}
	var began int64
	if err := s.db.QueryRow(`SELECT started_at FROM billable_intervals`).Scan(&began); err != nil || began != now.UnixNano() {
		t.Fatalf("compute start=%d, %v; want %d", began, err, now.UnixNano())
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 600, true)
}

func TestMeteredUnitRenewalCheckpointsAfterComputeStarts(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	base := *now
	var elapsed time.Duration
	s.clock = func() time.Time {
		elapsed++
		return base.Add(elapsed)
	}
	claim, _ := meteredUnitClaim(t, s, demand)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 1200, false)
}

func TestMeteredUnitIssuanceCannotOutliveClaimAuthorityProperty(t *testing.T) {
	for remaining := 1; remaining <= 48; remaining++ {
		s, demand, now := meteredUnitFixture(t, false)
		root := authorityRoot("short-lived", "a")
		root.RootMetered = true
		root.Grants = []capability.Grant{capability.TriggersClaim}
		expires := now.Add(time.Duration(remaining) * time.Microsecond)
		root.ExpiresAt = &expires
		authorityPersist(t, s, root)
		demand.CapabilityPrefix, demand.Target = root.Prefix, &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
		s.clock = func() time.Time {
			*now = now.Add(time.Microsecond)
			return *now
		}
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err == nil && !claim.Award.LeaseExpiresAt.After(*now) {
			t.Fatalf("award outlived authority with %d remaining clock steps: %+v, now=%s", remaining, claim.Award, *now)
		}
		if err != nil {
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
			rewriteSchemaCount(t, s.db, "credit_charges", 0)
		}
	}
}

func TestMeteredUnitOwnerWaitBillingFollowsPlacement(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, kind == capability.UnitPlan)
			demand.Target = &UnitRef{Kind: kind, RunID: "run"}
			if kind == capability.UnitPlan {
				rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
			}
			rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
			claim, actor := meteredUnitClaim(t, s, demand)
			for _, computing := range []bool{true, false, false, false, true, false} {
				if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, computing); err != nil {
					t.Fatal(err)
				}
				*now = now.Add(10 * time.Second)
			}
			finish := runstate.FinishRunRequest{Unit: actor.Owner, Transition: runstate.RunCompletion{RunID: "run", Cause: runstate.RunFinish, Actor: actor}}
			if kind == capability.UnitPlan {
			}
			for replay := range 2 {
				applied, err := (&Tenant{s: s, team: "a"}).FinishRunAndUnit(t.Context(), finish)
				if err != nil || applied.Replayed != (replay == 1) {
					t.Fatalf("owner finish: %+v, %v", applied, err)
				}
			}
			intervals := 2
			if kind == capability.UnitPlan {
				intervals = 1
			}
			unitBillingTotal(t, s, 120, true)
			rewriteSchemaCount(t, s.db, "billable_intervals", intervals)
		})
	}
}

func TestMeteredUnitReaperSettlesAtLeaseLapse(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	start := *now
	claim, actor := meteredUnitClaim(t, s, demand)
	admitExecution(t, &Tenant{s: s, team: "a"}, actor)
	if _, err := (&Tenant{s: s, team: "a"}).TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	*now = claim.Award.LeaseExpiresAt.Add(time.Hour)
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, max(60, int64(claim.Award.LeaseExpiresAt.Sub(start)/time.Second))*10, true)
	var end int64
	if err := s.db.QueryRow(`SELECT finished_at FROM billable_intervals`).Scan(&end); err != nil || end != claim.Award.LeaseExpiresAt.UnixNano() {
		t.Fatalf("interval ended at %d, %v; lease %v", end, err, claim.Award.LeaseExpiresAt)
	}
}

func TestMeteredUnitApplicationRetryKeepsOneReservation(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["sparkwing-4cpu"]},"retry":{"set":true,"value":1}}`))
	_, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	admitExecution(t, tenant, actor)
	start := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}
	if _, err := tenant.TransitionNode(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Second)
	retry := nodeTransition{Ref: start.Ref, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseRetry, SourceAttempt: sourceAttempt(1), Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: "verify", MaskedError: "try again"}}}
	if applied, err := tenant.TransitionNode(t.Context(), retry); err != nil || applied.Attempt != 2 {
		t.Fatalf("retry: %+v, %v", applied, err)
	}
	*now = now.Add(3 * time.Second)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: start.Ref, Actor: actor, To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(2)}); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 600, true)
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
	rewriteSchemaCount(t, s.db, "billable_intervals", 2)
	rewriteSchemaCount(t, s.db, "node_attempts", 2)
}

func TestMeteredUnitStaleReportCannotSettleReplacementProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(19, 928))
	for range 24 {
		s, demand, now := meteredUnitFixture(t, true)
		claim, actor := meteredUnitClaim(t, s, demand)
		if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Duration(1+random.IntN(10)) * time.Second)
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=?`, now.UnixNano())
		_, replacement := meteredUnitClaim(t, s, demand)
		actor.Generation += runspec.ClaimGeneration(random.IntN(2))
		if _, err := (&Tenant{s: s, team: "a"}).TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeFailed, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(0)}); err == nil {
			t.Fatal("stale report settled replacement")
		}
		unitBillingTotal(t, s, 600, false)
		row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil || row.CapabilityPrefix != replacement.CapabilityPrefix {
			t.Fatalf("replacement changed: %+v, %v", row, err)
		}
	}
}

// A controller outage settles a metered job at its funded lease rather than the longer default lease.
func TestMeteredLeaseNeverOutrunsWhatWasPaid(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	claim, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	paid := func() int64 {
		t.Helper()
		var amount int64
		if err := s.db.QueryRow(`SELECT COALESCE(SUM(amount_micro),0) FROM credit_charges`).Scan(&amount); err != nil {
			t.Fatal(err)
		}
		return amount
	}
	start := *now
	var lease time.Time
	for range 3 {
		*now = now.Add(20 * time.Second)
		beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
		if err != nil {
			t.Fatal(err)
		}
		row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil || row.LeaseExpiresAt == nil || !row.LeaseExpiresAt.Equal(beat.ExpiresAt) {
			t.Fatalf("beat %v differs from the lease %+v, %v", beat.ExpiresAt, row.LeaseExpiresAt, err)
		}
		if funded := start.Add(time.Duration(paid()/10) * time.Second); beat.ExpiresAt.After(funded) {
			t.Fatalf("lease runs to %v, past the %v its team paid for", beat.ExpiresAt, funded)
		}
		lease = beat.ExpiresAt
	}
	before := paid()
	*now = lease.Add(time.Hour)
	reaper, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().ReapUnits(t.Context(), reaper, 10); err != nil {
		t.Fatal(err)
	}
	if settled := paid(); settled > before {
		t.Fatalf("the lapse settled %d micro-credits beyond the %d paid before the controller went silent", settled-before, before)
	}
}
