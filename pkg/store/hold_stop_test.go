package store

import (
	"errors"
	"testing"
	"time"
)

func TestHoldCommitDurablyStopsRunningAward(t *testing.T) {
	for _, cause := range []string{HoldCauseOperator, HoldCauseDispute, HoldCauseWarning, HoldCauseRevoked} {
		t.Run(cause, func(t *testing.T) {
			s, demand, now := billingModelFixture(t)
			claim, actor := meteredUnitClaim(t, s, demand)
			tenant := &Tenant{s: s, team: "a"}
			if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				if cause == HoldCauseWarning {
					return holdForWarningTx(t.Context(), tx, "a", "warning-probe", "pi_probe", now.Add(-time.Hour))
				}
				if cause == HoldCauseRevoked {
					_, _, err := tenant.setBillingTrustTx(t.Context(), tx, BillingTrustChange{Trust: BillingTrustRevoked, Actor: "console", Reason: "probe"}, now.Add(-time.Hour))
					return err
				}
				actor := ""
				if cause == HoldCauseOperator {
					actor = "console"
				}
				_, err := holdCreditFreezeTx(t.Context(), tx, "a", "hold-probe", "", "probe", actor, now.Add(-time.Hour))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if run.CancelRequestedAt == nil {
				t.Error("hold committed without durable run cancellation")
			}
			var drain int64
			if err := s.db.QueryRow(`SELECT drain_deadline_at FROM billing_awards WHERE team='a' AND settled_at IS NULL`).Scan(&drain); err != nil || drain != now.Add(SliceSeconds*time.Second).UnixNano() {
				t.Fatalf("hold drain=%d, want store-time slice: %v", drain, err)
			}
			*now = now.Add(10 * time.Second)
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				_, err := holdCreditFreezeTx(t.Context(), tx, "a", "second-hold", "", "probe", "console", *now)
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
					_, _, err := tenant.setBillingTrustTx(t.Context(), tx, BillingTrustChange{Trust: BillingTrustAutomatic, Actor: "console", Reason: "release"}, *now)
					if err != nil {
						return err
					}
				}
				_, err := releaseCreditFreezesTx(t.Context(), tx, "a", "", false, false, "console", "probe", *now)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Second)
			beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
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
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	billingModelJob(t, s, "unstarted")
	demand.Target.NodeID = "unstarted"
	unstarted, holder := meteredUnitClaim(t, s, demand)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := holdCreditFreezeTx(t.Context(), tx, "a", "multiple-hold", "", "probe", "console", *now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var stopped, settled int
	if err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN settled_at IS NOT NULL THEN 1 ELSE 0 END),0) FROM billing_awards WHERE team='a' AND (stop_reason<>'' OR settled_at IS NOT NULL)`).Scan(&stopped, &settled); err != nil || stopped != 2 || settled != 1 {
		t.Fatalf("awards stopped/settled=%d/%d, want 2/1: %v", stopped, settled, err)
	}
	if _, err := tenant.ReleaseCreditFreezes(t.Context(), "multiple-hold", "console", "resolved", *now); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	beat, err := s.RenewUnit(t.Context(), unstarted.Award.CapabilityPrefix, true)
	var refusal *ErrIllegalTransition
	if err == nil && !beat.CancelRequested || err != nil && !errors.As(err, &refusal) {
		t.Fatalf("unstarted award regained authority after release: %+v, %v", beat, err)
	}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "unstarted"}, Actor: holder, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err == nil {
		t.Fatal("refunded unstarted award began executing after hold release")
	}
}

func TestHoldRollbackRestoresRunningAward(t *testing.T) {
	s, demand, now := billingModelFixture(t)
	claim, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("rollback hold")
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		if _, err := holdCreditFreezeTx(t.Context(), tx, "a", "rollback-probe", "", "probe", "console", *now); err != nil {
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
	beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
	if err != nil || beat.CancelRequested {
		t.Fatalf("rollback stopped renewal: %+v, %v", beat, err)
	}
}
