package wingd

import (
	"fmt"
	"testing"
	"time"
)

func TestEventWindow_SummaryFoldsOutcomes(t *testing.T) {
	var w eventWindow
	now := time.Now()
	w.record(now, admissionEvent{Kind: eventGrant, WaitMS: 1000})
	w.record(now, admissionEvent{Kind: eventGrant, WaitMS: 4000})
	w.record(now, admissionEvent{Kind: eventGrant, WaitMS: 9000})
	w.record(now, admissionEvent{Kind: eventEviction, Key: "land"})
	w.record(now, admissionEvent{Kind: eventEviction, Key: "land"})
	w.record(now, admissionEvent{Kind: eventEviction, Key: "deploy"})
	w.record(now, admissionEvent{Kind: eventQueueTimeout})
	w.record(now, admissionEvent{Kind: eventCancellation})
	w.record(now, admissionEvent{Kind: eventBackfill, BackfillCount: 1})
	w.record(now, admissionEvent{Kind: eventBackfill, BackfillCount: 2})

	s := w.summary(now)
	if s == nil {
		t.Fatal("summary nil for a populated window")
	}
	if s.Runs != 3 {
		t.Errorf("Runs = %d, want 3", s.Runs)
	}
	if s.MedianWaitMS != 4000 {
		t.Errorf("MedianWaitMS = %d, want 4000", s.MedianWaitMS)
	}
	if len(s.Evictions) != 2 || s.Evictions[0].Key != "deploy" || s.Evictions[0].Count != 1 ||
		s.Evictions[1].Key != "land" || s.Evictions[1].Count != 2 {
		t.Errorf("Evictions = %+v, want deploy:1, land:2 sorted by key", s.Evictions)
	}
	if s.QueueTimeouts != 1 || s.Cancellations != 1 {
		t.Errorf("timeouts/cancellations = %d/%d, want 1/1", s.QueueTimeouts, s.Cancellations)
	}
	if s.Backfills != 2 || s.BackfillProtections != 1 {
		t.Errorf("backfills/protections = %d/%d, want 2/1", s.Backfills, s.BackfillProtections)
	}
	if s.WindowMS != eventWindowSpan.Milliseconds() {
		t.Errorf("WindowMS = %d, want %d", s.WindowMS, eventWindowSpan.Milliseconds())
	}
}

func TestCancelledRunTombstonesRemainBoundedAndKeepNewest(t *testing.T) {
	d := &Daemon{cancelledRuns: map[string]struct{}{}}
	for i := 0; i <= maxCancelledRunTombstones; i++ {
		d.recordCancelledRunLocked(fmt.Sprintf("run-%05d", i))
	}
	if len(d.cancelledRunOrder) != maxCancelledRunTombstones {
		t.Fatalf("tombstones = %d, want %d", len(d.cancelledRunOrder), maxCancelledRunTombstones)
	}
	if _, exists := d.cancelledRuns["run-00000"]; exists {
		t.Fatal("oldest cancellation tombstone was not collected")
	}
	if _, exists := d.cancelledRuns[fmt.Sprintf("run-%05d", maxCancelledRunTombstones)]; !exists {
		t.Fatal("newest cancellation tombstone was collected")
	}
}

func TestEventWindow_SummaryFoldsRejectionsByCause(t *testing.T) {
	var w eventWindow
	now := time.Now()
	w.record(now, admissionEvent{Kind: eventRejection, Key: "cost_source"})
	w.record(now, admissionEvent{Kind: eventRejection, Key: "cost_source"})
	w.record(now, admissionEvent{Kind: eventRejection, Key: "request"})

	s := w.summary(now)
	if s == nil {
		t.Fatal("summary nil for a populated window")
	}
	if len(s.Rejections) != 2 ||
		s.Rejections[0].Cause != "cost_source" || s.Rejections[0].Count != 2 ||
		s.Rejections[1].Cause != "request" || s.Rejections[1].Count != 1 {
		t.Errorf("Rejections = %+v, want cost_source:2, request:1 sorted by cause", s.Rejections)
	}
}

func TestEventWindow_EmptyYieldsNilSummary(t *testing.T) {
	var w eventWindow
	if s := w.summary(time.Now()); s != nil {
		t.Errorf("empty window summary = %+v, want nil", s)
	}
}

func TestEventWindow_PrunesBySpanAndCap(t *testing.T) {
	var w eventWindow
	now := time.Now()
	w.record(now.Add(-25*time.Hour), admissionEvent{Kind: eventGrant, WaitMS: 1})
	w.record(now.Add(-time.Hour), admissionEvent{Kind: eventGrant, WaitMS: 2})
	s := w.summary(now)
	if s == nil || s.Runs != 1 {
		t.Fatalf("summary after span prune = %+v, want 1 surviving run", s)
	}

	var capped eventWindow
	for range eventWindowCap + 100 {
		capped.record(now, admissionEvent{Kind: eventGrant})
	}
	if got := len(capped.snapshot(now)); got != eventWindowCap {
		t.Errorf("entries after cap = %d, want %d", got, eventWindowCap)
	}
}
