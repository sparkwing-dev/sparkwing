package wingd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
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
	if s.WindowMS != eventWindowSpan.Milliseconds() {
		t.Errorf("WindowMS = %d, want %d", s.WindowMS, eventWindowSpan.Milliseconds())
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

func TestEventWindow_SurvivesStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Now()
	events := []admissionEvent{
		{At: now, Kind: eventGrant, WaitMS: 1234},
		{At: now, Kind: eventEviction, Key: "land"},
	}
	if err := writeState(path, admission.Snapshot{TotalMilliCores: 8000, TotalMemoryBytes: 1 << 30}, events); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	snap, restored, err := readState(path)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if snap == nil || snap.TotalMilliCores != 8000 {
		t.Fatalf("snapshot lost: %+v", snap)
	}
	if len(restored) != 2 || restored[0].WaitMS != 1234 || restored[1].Key != "land" {
		t.Fatalf("events lost across round trip: %+v", restored)
	}

	var w eventWindow
	w.restore(now, restored)
	s := w.summary(now)
	if s == nil || s.Runs != 1 || len(s.Evictions) != 1 {
		t.Errorf("restored summary = %+v, want 1 run and 1 eviction", s)
	}
}

// TestReadState_ToleratesEventlessFile pins that a state file written
// before the event window existed restores with an empty window.
func TestReadState_ToleratesEventlessFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeState(path, admission.Snapshot{TotalMilliCores: 4000, TotalMemoryBytes: 1 << 30}, nil); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	_, events, err := readState(path)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events = %+v, want none", events)
	}
}

func TestWaiterDepartureKind_ClassifiesTimeoutVersusCancel(t *testing.T) {
	now := time.Now()
	timedOut := &conn{queueTimeoutMS: 1000, startAt: now.Add(-2 * time.Second)}
	if got := waiterDepartureKindLocked(timedOut, now); got != eventQueueTimeout {
		t.Errorf("elapsed bounded wait = %q, want %q", got, eventQueueTimeout)
	}
	early := &conn{queueTimeoutMS: 60000, startAt: now.Add(-2 * time.Second)}
	if got := waiterDepartureKindLocked(early, now); got != eventCancellation {
		t.Errorf("early departure = %q, want %q", got, eventCancellation)
	}
	unbounded := &conn{startAt: now.Add(-time.Hour)}
	if got := waiterDepartureKindLocked(unbounded, now); got != eventCancellation {
		t.Errorf("unbounded waiter departure = %q, want %q", got, eventCancellation)
	}
}
