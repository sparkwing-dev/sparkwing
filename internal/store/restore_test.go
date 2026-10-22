package store

import (
	"context"
	"errors"
	"testing"
	"time"
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
