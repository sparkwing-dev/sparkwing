package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRestoreHoldsSchedulesUntilAcknowledgement(t *testing.T) {
	s := authorityStore(t)
	s.credentialEpoch = "original"
	ctx := t.Context()
	if err := s.EnsureCredentialEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := base.Add(time.Hour)
	s.clock = func() time.Time { return now }
	for _, team := range []Team{"a", "b"} {
		sched := CronSchedule{ID: "nightly", RepoPath: "https://example.com/acme/repo", Pipeline: "nightly", Cron: "* * * * *", CatchUp: 24 * time.Hour}
		tenant := &Tenant{s: s, team: team}
		if _, _, err := tenant.ArmCronSchedule(ctx, sched); err != nil {
			t.Fatal(err)
		}
	}
	// Rotation can survive a crash before prepare records its database half.
	s.credentialEpoch = "rotated"
	if err := s.EnsureCredentialEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	op := s.AsOperator()
	if ran, err := op.RunCronTickLeased(ctx, "restored", time.Minute, func(context.Context) error {
		t.Fatal("a held deployment ticked")
		return nil
	}); ran || !errors.Is(err, ErrRestorePending) {
		t.Fatalf("held tick = %t, %v", ran, err)
	}
	now = now.Add(2 * time.Hour)
	boundary := now
	if err := op.AcknowledgeRestore(ctx); err != nil {
		t.Fatal(err)
	}
	for _, team := range []Team{"a", "b"} {
		sched, err := (&Tenant{s: s, team: team}).GetCronSchedule(ctx, "nightly")
		if err != nil || !sched.CursorAt.Equal(boundary) || sched.NextDueAt != nil {
			t.Fatalf("acknowledged schedule = %+v, %v", sched, err)
		}
	}
	// An evaluator that loaded a due instant before acknowledgement cannot submit it afterwards.
	err := s.inTx(ctx, func(tx *storeTx) error { return checkRestoreScheduleTx(ctx, tx, base.Add(time.Minute)) })
	if !errors.Is(err, ErrRestorePending) {
		t.Fatalf("stale due admission = %v", err)
	}
	err = s.inTx(ctx, func(tx *storeTx) error { return checkRestoreScheduleTx(ctx, tx, boundary.Add(time.Minute)) })
	if err != nil {
		t.Fatalf("future due refused: %v", err)
	}
	now = now.Add(time.Hour)
	if err := op.AcknowledgeRestore(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := op.RestoreStatus(ctx)
	if err != nil || state.Pending || state.AcknowledgedAt != boundary.UnixNano() {
		t.Fatalf("repeated acknowledgement moved boundary = %+v, %v", state, err)
	}
	if err := s.EnsureCredentialEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = op.RestoreStatus(ctx)
	if err != nil || state.Pending {
		t.Fatalf("same epoch reentered restore = %+v, %v", state, err)
	}
}

func TestRestorePreparationCancelsScheduledWorkBeforeClaims(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitNode} {
		for _, restored := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restored=%t", kind, restored), func(t *testing.T) {
				var s *Store
				var tenant *Tenant
				var demand UnitDemand
				if kind == capability.UnitNode {
					s, demand = unitClaimFixture(t)
					fundUnitClaim(t, s)
					tenant = &Tenant{s: s, team: "a"}
				} else {
					s, tenant, demand, _ = cancellationFixture(t, kind)
				}
				rewriteSchemaExec(t, s.db, `UPDATE runs SET trigger_source='schedule'`)
				if restored {
					if err := s.AsOperator().PrepareRestore(t.Context(), "rotated"); err != nil {
						t.Fatal(err)
					}
					run, err := tenant.ReadRun(t.Context(), "run")
					if err != nil || run.CancelRequestedAt == nil || run.CancelReason != "cancelled by restore; rerun if needed" {
						t.Fatalf("restored run cancellation: at=%v reason=%q err=%v", run.CancelRequestedAt, run.CancelReason, err)
					}
				}
				claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
				if restored && err == nil {
					t.Fatal("restored scheduled work was claimed")
				}
				if !restored && err != nil {
					t.Fatalf("backup work was not claimable: %v", err)
				}
				if !restored {
					if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
