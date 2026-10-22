package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestHoldCommitDurablyStopsRunningAward(t *testing.T) {
	for _, cause := range []string{HoldCauseOperator, HoldCauseDispute, HoldCauseWarning, HoldCauseRevoked} {
		t.Run(cause, func(t *testing.T) {
			s, demand, now := billingModelFixture(t)
			if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentTest); err != nil {
				t.Fatal(err)
			}
			claim, actor := meteredUnitClaim(t, s, demand)
			tenant := &Tenant{s: s, team: "a"}
			admitExecution(t, tenant, actor)
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				if cause == HoldCauseWarning {
					return holdForWarningTx(t.Context(), tx, "a", "warning-probe", "pi_probe", now.Add(-time.Hour))
				}
				if cause == HoldCauseRevoked {
					_, _, err := tenant.setBillingTrustTx(t.Context(), tx, BillingTrustChange{Trust: BillingTrustRevoked, Actor: "console", Reason: "probe"}, now.Add(-time.Hour), auditActor{system: auditBreakGlass})
					return err
				}
				actor := ""
				if cause == HoldCauseOperator {
					actor = "console"
				}
				_, err := holdCreditFreezeTx(t.Context(), tx, "a", "hold-probe", "", "probe", actor, now.Add(-time.Hour), auditActor{system: auditBreakGlass})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if run.CancelRequestedAt != nil {
				t.Error("hold cancelled the whole run")
			}
			node, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || node.CancelRequestedAt == nil {
				t.Fatalf("hold committed without durable job cancellation: %+v %v", node, err)
			}
			var drain int64
			if err := s.db.QueryRow(`SELECT drain_deadline_at FROM billing_awards WHERE team='a' AND settled_at IS NULL`).Scan(&drain); err != nil || drain != now.Add(60*time.Second).UnixNano() {
				t.Fatalf("hold drain=%d, want store-time slice: %v", drain, err)
			}
			*now = now.Add(10 * time.Second)
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				_, err := holdCreditFreezeTx(t.Context(), tx, "a", "second-hold", "", "probe", "console", *now, auditActor{system: auditBreakGlass})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var repeated int64
			if err := s.db.QueryRow(`SELECT drain_deadline_at FROM billing_awards WHERE team='a' AND settled_at IS NULL`).Scan(&repeated); err != nil || repeated != drain {
				t.Fatalf("second hold changed drain from %d to %d: %v", drain, repeated, err)
			}
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				if cause == HoldCauseRevoked {
					_, _, err := tenant.setBillingTrustTx(t.Context(), tx, BillingTrustChange{Trust: BillingTrustAutomatic, Actor: "console", Reason: "release"}, *now, auditActor{system: auditBreakGlass})
					if err != nil {
						return err
					}
				}
				_, err := releaseCreditFreezesTx(t.Context(), tx, "a", "", false, false, "console", "probe", *now, auditActor{system: auditBreakGlass})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Second)
			beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil || !beat.CancelRequested {
				t.Errorf("hold released before renewal evaded stop: %+v, %v", beat, err)
			}
		})
	}
}

func TestHoldStopsRunningAndUnstartedAwardsTogether(t *testing.T) {
	s, demand, now := billingModelFixture(t)
	_, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	billingModelJob(t, s, "unstarted")
	demand.Target.NodeID = "unstarted"
	unstarted, holder := meteredUnitClaim(t, s, demand)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := holdCreditFreezeTx(t.Context(), tx, "a", "multiple-hold", "", "probe", "console", *now, auditActor{system: auditBreakGlass})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var stopped, settled int
	if err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN settled_at IS NOT NULL THEN 1 ELSE 0 END),0) FROM billing_awards WHERE team='a' AND (stop_reason<>'' OR settled_at IS NOT NULL)`).Scan(&stopped, &settled); err != nil || stopped != 2 || settled != 1 {
		t.Fatalf("awards stopped/settled=%d/%d, want 2/1: %v", stopped, settled, err)
	}
	s.SetTestClock(t, func() time.Time { return *now })
	if _, err := tenant.ReleaseCreditFreezes(t.Context(), "multiple-hold", "console", "resolved"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	beat, err := s.AsOperator().RenewUnit(t.Context(), unstarted.Award.CapabilityPrefix, true)
	var refusal *ErrIllegalTransition
	if err == nil && !beat.CancelRequested || err != nil && !errors.As(err, &refusal) {
		t.Fatalf("unstarted award regained authority after release: %+v, %v", beat, err)
	}
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "unstarted"}, Actor: holder, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err == nil {
		t.Fatal("refunded unstarted award began executing after hold release")
	}
}

func TestHoldRollbackRestoresRunningAward(t *testing.T) {
	s, demand, now := billingModelFixture(t)
	claim, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("rollback hold")
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		if _, err := holdCreditFreezeTx(t.Context(), tx, "a", "rollback-probe", "", "probe", "console", *now, auditActor{system: auditBreakGlass}); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.CancelRequestedAt != nil {
		t.Fatalf("rollback kept cancellation: %+v, %v", run.CancelRequestedAt, err)
	}
	var stop string
	var drain any
	if err := s.db.QueryRow(`SELECT stop_reason,drain_deadline_at FROM billing_awards WHERE team='a' AND settled_at IS NULL`).Scan(&stop, &drain); err != nil || stop != "" || drain != nil {
		t.Fatalf("rollback kept award stop %q/%v: %v", stop, drain, err)
	}
	*now = now.Add(time.Second)
	beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
	if err != nil || beat.CancelRequested {
		t.Fatalf("rollback stopped renewal: %+v, %v", beat, err)
	}
}

func TestFundingStoppedJobPreservesOwnerAndIndependentJobs(t *testing.T) {
	for _, refusal := range []string{"balance", "hold"} {
		t.Run(refusal, func(t *testing.T) {
			tenant, request := sharedSettingsPlan(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
				{ID: "cloud"}, {ID: "customer"}, {ID: "next", Needs: []string{"customer"}}, {ID: "blocked", Needs: []string{"cloud"}},
			}})
			s := tenant.s
			now := s.now()
			s.clock = func() time.Time { return now }
			if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: runspec.RunID(request.RunID), To: RunRunning, Cause: RunStart, Actor: request.Actor}); err != nil {
				t.Fatal(err)
			}
			fundUnitClaim(t, s)
			for _, prefix := range []string{"cloud-runner", "customer-runner"} {
				root := authorityRoot(prefix, "a")
				root.Grants = []capability.Grant{capability.NodesClaim}
				if prefix == "cloud-runner" {
					root.Subject.Kind, root.Team, root.RootMetered = capability.SubjectService, "", true
					root.Grants = []capability.Grant{capability.ClaimsLaunch}
				}
				authorityPersist(t, s, root)
			}
			start := func(id, prefix string) (UnitClaim, Actor) {
				t.Helper()
				if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: runspec.NodeID(id)}, To: NodeReady, Cause: CauseRelease, Actor: request.Actor}); err != nil {
					t.Fatal(err)
				}
				claim, actor := meteredUnitClaim(t, s, UnitDemand{CapabilityPrefix: prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(request.RunID), NodeID: runspec.NodeID(id)}})
				admitExecution(t, tenant, actor)
				if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: runspec.NodeID(id)}, To: NodeRunning, Cause: CauseStart, Actor: actor, SourceAttempt: sourceAttempt(0)}); err != nil {
					t.Fatal(err)
				}
				return claim, actor
			}
			cloud, _ := start("cloud", "cloud-runner")
			customer, customerActor := start("customer", "customer-runner")
			if refusal == "hold" {
				if _, err := tenant.TestOnlyHoldForDispute(t.Context(), "mixed", "", "dispute opened", now); err != nil {
					t.Fatal(err)
				}
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=(SELECT SUM(amount_micro) FROM credit_charges WHERE team='a') WHERE team='a'`)
			}
			now = now.Add(10 * time.Second)
			beat, err := s.AsOperator().RenewUnit(t.Context(), cloud.Award.CapabilityPrefix, true)
			if err != nil || !beat.CancelRequested {
				t.Fatalf("Cloud stop: %+v %v", beat, err)
			}
			if beat, err := tenant.RenewOwner(t.Context(), request.Actor); err != nil || beat.CancelRequested {
				t.Fatalf("job stop cancelled its owner: %+v %v", beat, err)
			}
			if beat, err := s.AsOperator().RenewUnit(t.Context(), customer.Award.CapabilityPrefix, true); err != nil || beat.CancelRequested {
				t.Fatalf("independent customer job cancelled: %+v %v", beat, err)
			}
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "customer"}, Actor: customerActor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(1)}); err != nil {
				t.Fatal(err)
			}
			_, next := start("next", "customer-runner")
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "next"}, Actor: next, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(1)}); err != nil {
				t.Fatal(err)
			}
			now = beat.ExpiresAt
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "blocked"}, To: NodeSkipped, Cause: causeDependency, Actor: request.Actor}); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.FinishRunAndUnit(t.Context(), FinishRunRequest{Unit: request.Actor.Owner, Transition: RunCompletion{RunID: runspec.RunID(request.RunID), Cause: RunFinish, Actor: request.Actor}}); err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), request.RunID)
			if err != nil || run.State != RunFailed || run.CancelRequestedAt != nil {
				t.Fatalf("job stop changed whole-run cancellation: state=%s cancelled=%v, %v", run.State, run.CancelRequestedAt, err)
			}
		})
	}
}
