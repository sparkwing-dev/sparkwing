package store

import (
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
	table := CreditRateTable{{Cores: 1, MicroPerSecond: 2}, {Cores: 4, MicroPerSecond: 10}}
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
		t.Fatal(err)
	}
	s.billing = unitBillingCalculation
	return s, demand, &now
}

func unitBillingCalculation(p capability.BillingProvenance, intervals []capability.BillableInterval) (capability.BillingSettlement, error) {
	result := capability.BillingSettlement{ReservationID: p.ReservationID, RefundUnstarted: len(intervals) == 0}
	if !p.Metered || len(intervals) == 0 {
		return result, nil
	}
	var elapsed time.Duration
	for _, interval := range intervals {
		elapsed += interval.End.Sub(interval.Start)
	}
	result.BillableSeconds = int64((elapsed + time.Second - 1) / time.Second)
	for _, rule := range capability.BillingRules {
		if rule.Unit == p.Unit {
			result.BillableSeconds = max(result.BillableSeconds, rule.MinimumSeconds)
		}
	}
	return result, nil
}

func meteredUnitClaim(t *testing.T, s *Store, demand UnitDemand) (UnitClaim, Actor) {
	t.Helper()
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	if principal.Capability.Delivery == capability.DeliveryBootstrap {
		issued, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
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

func TestUnmeteredUnitLifecycleNeedsNoCalculator(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		for _, end := range []string{"release", "unstarted_lapse", "started_lapse"} {
			t.Run(string(kind)+"/"+end, func(t *testing.T) {
				s, _, claim, now := renewalFixture(t, kind)
				s.billing = func(capability.BillingProvenance, []capability.BillableInterval) (capability.BillingSettlement, error) {
					t.Error("unmetered lifecycle called the billing calculator")
					return capability.BillingSettlement{}, errors.New("no unmetered calculator")
				}
				started := end == "started_lapse"
				if started && kind == capability.UnitNode {
					ordinal := int64(0)
					actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
					if _, err := (&Tenant{s: s, team: claim.Team}).TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{claim.RunID, claim.NodeID}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: &ordinal}); err != nil {
						t.Fatal(err)
					}
				}
				beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, started)
				if err != nil {
					t.Fatal(err)
				}
				if end == "release" {
					if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
						t.Fatal(err)
					}
				} else {
					*now = beat.ExpiresAt
					lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					units, err := s.ReapUnits(t.Context(), lease, 10)
					if err != nil || len(units) != 1 || !units[0].Requeued {
						t.Fatalf("unmetered recovery: %+v, %v", units, err)
					}
				}
				unitBillingTotal(t, s, 0, true)
				rewriteSchemaCount(t, s.db, "billable_intervals", 0)
			})
		}
	}
}

func TestMeteredUnitClaimAndReservationCommitTogether(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, false)
	rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err == nil || len(claim.Bearer) != 0 {
		t.Fatalf("unfunded award: %+v, %v", claim, err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 0)
	rewriteSchemaCount(t, s.db, "credit_charges", 0)
	row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), NodeRef{"run", "job"})
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
			request.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: node}
			_, err := s.ClaimUnit(t.Context(), request)
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
	claim, err := s.ClaimUnit(t.Context(), demand)
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
	s.billing = unitBillingCalculation
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
	rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,capability_prefix,lease_expires_at,owner_generation) VALUES ('a','run','job',2,'claimed',8,'eval-host','evaluation',?,4)`, now.Add(time.Minute).UnixNano())
	if _, err := s.RenewUnit(t.Context(), c.Prefix, true); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Second)
	request.Actor = Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix}
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
			started, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)})
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
			table := CreditRateTable{{Cores: 1, MicroPerSecond: 200}, {Cores: 4, MicroPerSecond: 1000}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(10 * time.Second)
			finish := NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(started.Attempt)}
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
	if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), NodeRef{"run", "job"})
	if err != nil || row.Status != StatusPending || row.Attempt != 0 || row.StartedAt != nil {
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
	if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 600, false)
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
		claim, err := s.ClaimUnit(t.Context(), demand)
		if err == nil && !claim.Award.LeaseExpiresAt.After(*now) {
			t.Fatalf("award outlived authority with %d remaining clock steps: %+v, now=%s", remaining, claim.Award, *now)
		}
		if err != nil {
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
			rewriteSchemaCount(t, s.db, "credit_charges", 0)
		}
	}
}

func TestMeteredUnitOwnerWaitsDoNotAccrueCompute(t *testing.T) {
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
				if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, computing); err != nil {
					t.Fatal(err)
				}
				*now = now.Add(10 * time.Second)
			}
			finish := FinishRunRequest{Unit: actor.Owner, Transition: RunTransition{RunID: "run", To: RunSuccess, Cause: RunFinish, Actor: actor}}
			for replay := range 2 {
				applied, err := (&Tenant{s: s, team: "a"}).FinishRunAndUnit(t.Context(), finish)
				if err != nil || applied.Replayed != (replay == 1) {
					t.Fatalf("owner finish: %+v, %v", applied, err)
				}
			}
			want := int64(40)
			if kind == capability.UnitTrigger {
				want = 120
			}
			unitBillingTotal(t, s, want, true)
			rewriteSchemaCount(t, s.db, "billable_intervals", 2)
		})
	}
}

func TestMeteredUnitReaperSettlesAtLeaseLapse(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	start := *now
	claim, actor := meteredUnitClaim(t, s, demand)
	if _, err := (&Tenant{s: s, team: "a"}).TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	*now = claim.Award.LeaseExpiresAt.Add(time.Hour)
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReapUnits(t.Context(), lease, 10); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, max(60, int64(claim.Award.LeaseExpiresAt.Sub(start)/time.Second))*10, true)
	var end int64
	if err := s.db.QueryRow(`SELECT finished_at FROM billable_intervals`).Scan(&end); err != nil || end != claim.Award.LeaseExpiresAt.UnixNano() {
		t.Fatalf("interval ended at %d, %v; lease %v", end, err, claim.Award.LeaseExpiresAt)
	}
}

func TestMeteredUnitSettlementFailureRollsBackResult(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, false)
	_, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("billing unavailable")
	s.billing = func(capability.BillingProvenance, []capability.BillableInterval) (capability.BillingSettlement, error) {
		return capability.BillingSettlement{}, boom
	}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(1)}); !errors.Is(err, boom) {
		t.Fatalf("settlement error: %v", err)
	}
	row, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
	if err != nil || row.Status != StatusRunning || row.Attempt != 1 || row.StateSeq != 2 {
		t.Fatalf("failed settlement changed job: %+v, %v", row, err)
	}
	unitBillingTotal(t, s, 600, false)
}

func TestMeteredUnitApplicationRetryKeepsOneReservation(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":4}}},"retry":{"set":true,"value":1}}`))
	_, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	start := NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}
	if _, err := tenant.TransitionNode(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Second)
	retry := NodeTransition{Ref: start.Ref, Actor: actor, To: NodeRunning, Cause: CauseRetry, SourceAttempt: sourceAttempt(1), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: "verify", MaskedError: "try again"}}}
	if applied, err := tenant.TransitionNode(t.Context(), retry); err != nil || applied.Attempt != 2 {
		t.Fatalf("retry: %+v, %v", applied, err)
	}
	*now = now.Add(3 * time.Second)
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: start.Ref, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(2)}); err != nil {
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
		actor.Generation += int64(random.IntN(2))
		if _, err := (&Tenant{s: s, team: "a"}).TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeFailed, Cause: CauseReport, SourceAttempt: sourceAttempt(0)}); err == nil {
			t.Fatal("stale report settled replacement")
		}
		unitBillingTotal(t, s, 600, false)
		row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), NodeRef{"run", "job"})
		if err != nil || row.CapabilityPrefix != replacement.CapabilityPrefix {
			t.Fatalf("replacement changed: %+v, %v", row, err)
		}
	}
}
