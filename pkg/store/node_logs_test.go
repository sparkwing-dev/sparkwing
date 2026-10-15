package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
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

	for _, seg := range []store.NodeLogSegment{segment(4, 300, "d"), segment(2, 100, "b")} {
		if err := s.SignNodeLogSegment(ctx, open, seg, 1<<20, false, now); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := s.LatestNodeLog(ctx, team, f.run, "a")
	if err != nil || latest.UploadedThrough != 4 || latest.StoredBytes != 400 || latest.ChargedBytes != 0 {
		t.Fatalf("progress = %+v, %v; want through 4, 400 stored, nothing charged", latest, err)
	}
	if _, err := s.LatestNodeLog(ctx, team, f.run, "b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a node that wrote nothing has a log: %v", err)
	}
	if _, err := s.LatestNodeLog(ctx, "other", f.run, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another team reads the log: %v", err)
	}

	if err := s.MarkNodeLogSealed(ctx, open, 512, now); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.LatestNodeLog(ctx, team, f.run, "a"); l.SealedAt.IsZero() || l.StoredBytes != 512 {
		t.Fatalf("sealed = %+v", l)
	}
	if err := s.SignNodeLogSegment(ctx, open, segment(5, 1, "e"), 1<<20, false, now); !errors.Is(err, store.ErrNodeLogSealed) {
		t.Fatalf("a segment after the seal = %v, want ErrNodeLogSealed", err)
	}
	if err := s.DeleteRunNodeLogs(ctx, team, f.run); err != nil {
		t.Fatal(err)
	}
	if logs, err := s.RunNodeLogs(ctx, team, f.run); err != nil || len(logs) != 0 {
		t.Fatalf("records outlived the run's logs: %+v, %v", logs, err)
	}
	if segs, err := s.NodeLogSegments(ctx, open); err != nil || len(segs) != 0 {
		t.Fatalf("signed segments outlived the run's logs: %+v, %v", segs, err)
	}
}

func segment(n int, size int64, digest string) store.NodeLogSegment {
	return store.NodeLogSegment{N: n, StoredBytes: size, MD5: "md5-" + digest, SHA256: "sha-" + digest}
}

// Signing a segment charges the team exactly its size once: signing it
// again for the same bytes is free, other bytes under its number and a
// segment past the cap are refused and charge nothing.
func TestSigningALogSegmentChargesItsSizeOnce(t *testing.T) {
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
	charged := func() int64 {
		t.Helper()
		got, err := s.GetNodeLog(ctx, store.DefaultTeam, f.run, "a", attempt, gen)
		if err != nil {
			t.Fatal(err)
		}
		if got.ChargedBytes != got.StoredBytes {
			t.Fatalf("charged %d of %d stored bytes", got.ChargedBytes, got.StoredBytes)
		}
		return got.ChargedBytes
	}
	for _, step := range []struct {
		seg     store.NodeLogSegment
		wantErr error
		want    int64
	}{
		{segment(1, 100, "a"), nil, 100},
		{segment(1, 100, "a"), nil, 100},
		{segment(1, 100, "other"), store.ErrNodeLogSegmentConflict, 100},
		{segment(1, 101, "a"), store.ErrNodeLogSegmentConflict, 100},
		{segment(2, 50, "b"), nil, 150},
		{segment(3, 51, "c"), store.ErrNodeLogCap, 150},
		{segment(3, 50, "c"), nil, 200},
	} {
		if err := s.SignNodeLogSegment(ctx, rec, step.seg, 200, true, time.Now()); !errors.Is(err, step.wantErr) {
			t.Fatalf("sign %+v = %v, want %v", step.seg, err, step.wantErr)
		}
		if got := charged(); got != step.want {
			t.Fatalf("after signing %+v the attempt holds %d charged bytes, want %d", step.seg, got, step.want)
		}
	}
	if segs, err := s.NodeLogSegments(ctx, rec); err != nil || len(segs) != 3 || segs[0] != segment(1, 100, "a") {
		t.Fatalf("signed segments = %+v, %v", segs, err)
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

// An ended claim keeps log authority over its own attempt for the seal
// grace after the attempt ended, and none after it.
func TestAnEndedClaimKeepsItsLogTailForTheGrace(t *testing.T) {
	f := newDispatchRun(t, "run-tail")
	f.mustAccept(t, planOf("a"))
	raw := f.claimRaw(t, "a", store.ClaimTokenWork)
	ctx, s := t.Context(), f.s
	live, err := s.AuthorizeClaimToken(ctx, raw, store.ClaimLogTail, time.Now())
	if err != nil || live.Ended {
		t.Fatalf("a live claim = %+v, %v", live, err)
	}
	if _, err := f.report(live, store.AttemptReport{Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthorizeClaimToken(ctx, raw, store.ClaimReporting, time.Now()); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("the ended claim reports = %v, want ErrClaimNotLive", err)
	}
	tail, err := s.AuthorizeClaimToken(ctx, raw, store.ClaimLogTail, time.Now())
	if err != nil || !tail.Ended || tail.Attempt != 1 {
		t.Fatalf("the ended claim's tail = %+v, %v; want attempt 1, ended", tail, err)
	}
	if _, err := s.AuthorizeClaimToken(ctx, raw, store.ClaimLogTail, time.Now().Add(store.NodeLogSealGrace)); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("the tail past the grace = %v, want ErrClaimNotLive", err)
	}

	backdate := func(q string, at time.Time) {
		t.Helper()
		if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, q), at.UnixNano(), f.run); err != nil {
			t.Fatal(err)
		}
	}
	backdate(`UPDATE node_execution_attempts SET finished_at = ? WHERE run_id = ?`, time.Now().Add(-2*store.NodeLogSealGrace))
	backdate(`UPDATE nodes SET finished_at = ? WHERE run_id = ? AND node_id = 'a'`, time.Now())
	if tail, err := s.AuthorizeClaimToken(ctx, raw, store.ClaimLogTail, time.Now()); err != nil || !tail.Ended {
		t.Fatalf("a tail within the grace of the node's later end = %+v, %v", tail, err)
	}
}
