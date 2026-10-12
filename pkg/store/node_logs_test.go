package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestNodeLogRecordFollowsTheRunningAttempt(t *testing.T) {
	f := newDispatchRun(t, "run-logs")
	f.mustAccept(t, planOf("a", "b"))
	f.claim(t, "a", store.ClaimTokenWork)
	ctx, s, team := t.Context(), f.s, store.DefaultTeam
	attempt, gen, err := s.CurrentNodeAttempt(ctx, team, f.run, "a")
	if err != nil || attempt != 1 || gen < 1 {
		t.Fatalf("current attempt = %d gen %d, %v", attempt, gen, err)
	}
	open := store.NodeLog{Team: team, RunID: f.run, NodeID: "a", Attempt: attempt, Generation: gen, Prefix: "first/"}
	now := time.Now()
	got, err := s.OpenNodeLog(ctx, open, now)
	if err != nil || got.Prefix != "first/" || !got.SealedAt.IsZero() {
		t.Fatalf("open = %+v, %v", got, err)
	}
	moved := open
	moved.Prefix = "second/"
	if got, err := s.OpenNodeLog(ctx, moved, now); err != nil || got.Prefix != "first/" {
		t.Fatalf("reopen moved the log: %+v, %v", got, err)
	}

	progress := open
	progress.UploadedThrough, progress.StoredBytes = 4, 400
	if err := s.RecordNodeLogProgress(ctx, progress, now); err != nil {
		t.Fatal(err)
	}
	progress.UploadedThrough, progress.StoredBytes = 2, 100
	if err := s.RecordNodeLogProgress(ctx, progress, now); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestNodeLog(ctx, team, f.run, "a")
	if err != nil || latest.UploadedThrough != 4 || latest.StoredBytes != 400 {
		t.Fatalf("progress went down: %+v, %v", latest, err)
	}
	if _, err := s.LatestNodeLog(ctx, team, f.run, "b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a node that wrote nothing has a log: %v", err)
	}
	if _, err := s.LatestNodeLog(ctx, "other", f.run, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another team reads the log: %v", err)
	}

	if abandoned, err := s.AbandonedNodeLogs(ctx, now.Add(time.Hour), 10); err != nil || len(abandoned) != 0 {
		t.Fatalf("a running attempt reads as abandoned: %+v, %v", abandoned, err)
	}
	if err := s.MarkNodeLogSealed(ctx, open, 512, now); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.LatestNodeLog(ctx, team, f.run, "a"); l.SealedAt.IsZero() || l.StoredBytes != 512 {
		t.Fatalf("sealed = %+v", l)
	}
	if err := s.DeleteRunNodeLogs(ctx, team, f.run); err != nil {
		t.Fatal(err)
	}
	if logs, err := s.RunNodeLogs(ctx, team, f.run); err != nil || len(logs) != 0 {
		t.Fatalf("records outlived the run's logs: %+v, %v", logs, err)
	}
}

func TestAbandonedNodeLogsWaitForTheGraceAfterTheNodeFinished(t *testing.T) {
	f := newDispatchRun(t, "run-abandoned")
	f.mustAccept(t, planOf("a"))
	tok := f.claim(t, "a", store.ClaimTokenWork)
	ctx, s := t.Context(), f.s
	attempt, gen, err := s.CurrentNodeAttempt(ctx, store.DefaultTeam, f.run, "a")
	if err != nil {
		t.Fatal(err)
	}
	opened := time.Now().Add(-time.Hour)
	if _, err := s.OpenNodeLog(ctx, store.NodeLog{
		Team: store.DefaultTeam, RunID: f.run, NodeID: "a",
		Attempt: attempt, Generation: gen, Prefix: "p/",
	}, opened); err != nil {
		t.Fatal(err)
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.AbandonedNodeLogs(ctx, time.Now().Add(-time.Minute), 10); err != nil || len(got) != 0 {
		t.Fatalf("a node that just finished reads as abandoned: %+v, %v", got, err)
	}
	got, err := s.AbandonedNodeLogs(ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(got) != 1 || got[0].NodeID != "a" {
		t.Fatalf("abandoned = %+v, %v", got, err)
	}
}

func TestNodeLogChargesOnceAndPrunesFinishedRunsOldestFirst(t *testing.T) {
	f := newDispatchRun(t, "run-charge")
	f.mustAccept(t, planOf("a"))
	tok := f.claim(t, "a", store.ClaimTokenWork)
	ctx, s := t.Context(), f.s
	attempt, gen, err := s.CurrentNodeAttempt(ctx, store.DefaultTeam, f.run, "a")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.OpenNodeLog(ctx, store.NodeLog{
		Team: store.DefaultTeam, RunID: f.run, NodeID: "a",
		Attempt: attempt, Generation: gen, Prefix: "p/",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ChargeNodeLog(ctx, rec, 100, time.Now()); err != nil || !ok {
		t.Fatalf("first charge = %v, %v", ok, err)
	}
	if ok, err := s.ChargeNodeLog(ctx, rec, 150, time.Now()); err != nil || ok {
		t.Fatalf("a charge from a stale read = %v, %v; want refused", ok, err)
	}
	if got, err := s.GetNodeLog(ctx, store.DefaultTeam, f.run, "a", attempt, gen); err != nil || got.ChargedBytes != 100 {
		t.Fatalf("charged = %+v, %v", got, err)
	}
	if prunable, err := s.PrunableNodeLogs(ctx, store.DefaultTeam, 10); err != nil || len(prunable) != 0 {
		t.Fatalf("a running run's logs are prunable: %+v, %v", prunable, err)
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	if prunable, err := s.PrunableNodeLogs(ctx, store.DefaultTeam, 10); err != nil || len(prunable) != 1 {
		t.Fatalf("a finished run's logs = %+v, %v", prunable, err)
	}
}
