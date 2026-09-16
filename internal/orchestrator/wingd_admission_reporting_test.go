package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestFormatQueueWaitLeadsWithMachineState(t *testing.T) {
	clear := int64(100_000)
	qs := wingwire.QueueState{
		Resources: []wingwire.ResourceState{{
			Key: "cores", Capacity: 16, Held: 6.6, Reserved: 1, External: 7.3,
			ExternalSource: wingwire.ExternalMeasured, Available: 1.1,
		}},
		Holders: []wingwire.Holder{
			{RunID: "run-a", Pipeline: "pre-push", Resources: wingwire.HostResources{Cores: 5.2}},
			{RunID: "run-b", Pipeline: "docs-build", Resources: wingwire.HostResources{Cores: 1.4}},
			{RunID: "connected", Pipeline: "parent", ConnectionOnly: true},
			{RunID: "waiting-parent", Pipeline: "gate", AdmissionWaiting: true},
		},
		Waiters:         []wingwire.Waiter{{RunID: "waiting", Position: 1, WaitingOn: []string{"cores"}}},
		ExpectedClearMS: &clear,
	}
	got := formatQueueWait(
		wingwire.AdmissionRequest{Resources: wingwire.HostResources{Cores: 8}, CostSource: wingwire.CostSourcePin},
		"waiting", "waiting", wingwire.Queued{Position: 1, QueueLength: 1}, qs, true, 0,
	)
	want := "admission: 2 running (pre-push 5.2 cores, docs-build 1.4 cores), 1 queued; " +
		"you are next -- needs 8.0 cores (pinned); 1.1 free, 6.6 held, external 7.30; expected clear ~1m40s"
	if got != want {
		t.Fatalf("queue line:\n got: %s\nwant: %s", got, want)
	}
}

func TestFormatQueueWaitShowsOnlyDeeperPositions(t *testing.T) {
	qs := wingwire.QueueState{
		Resources: []wingwire.ResourceState{{
			Key: "memory", Capacity: float64(16 << 30), Held: float64(8 << 30), Reserved: float64(2 << 30),
			External: float64(4 << 30), ExternalSource: wingwire.ExternalMeasured, Available: float64(2 << 30),
		}},
		Waiters: make([]wingwire.Waiter, 5),
	}
	got := formatQueueWait(
		wingwire.AdmissionRequest{Resources: wingwire.HostResources{MemoryBytes: 2 << 30}, CostSource: wingwire.CostSourceMeasured},
		"waiting", "waiting", wingwire.Queued{Key: "memory", Position: 3, QueueLength: 5}, qs, true, 90*time.Second,
	)
	for _, want := range []string{
		"0 running, 5 queued", "position 3 of 5", "needs 2.0 GiB (measured)",
		"2.0 GiB free, 8.0 GiB held, external 4.0 GiB", "waited 1m30s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("queue line omitted %q: %s", want, got)
		}
	}
	for _, absent := range []string{"participants ahead", "expected clear", "you are next"} {
		if strings.Contains(got, absent) {
			t.Errorf("queue line contains %q: %s", absent, got)
		}
	}
}

func TestQueueWaitReporterEmitsOnlyMaterialChanges(t *testing.T) {
	clearA, clearB := int64(100_000), int64(99_000)
	states := []wingwire.QueueState{
		queueReporterState(1.1, 7.3, []string{"cores"}, &clearA, "pre-push"),
		queueReporterState(1.2, 7.2, []string{"cores"}, &clearB, "pre-push"),
		queueReporterState(1.3, 7.1, []string{"memory"}, &clearB, "pre-push"),
		queueReporterState(6.5, 1.9, []string{"memory"}, &clearB, "docs-build"),
	}
	var out strings.Builder
	next := 0
	reporter := &queueWaitReporter{
		la: &LocalAdmission{Out: &out}, requestID: "waiting", displayID: "waiting",
		request: wingwire.AdmissionRequest{RunID: "waiting", Resources: wingwire.HostResources{Cores: 8}, CostSource: wingwire.CostSourcePin},
		seen:    true, since: time.Now(), latest: wingwire.Queued{Position: 1, QueueLength: 1},
		snapshot: func(context.Context) (wingwire.QueueState, error) {
			state := states[next]
			next++
			return state, nil
		},
	}

	reporter.emitUpdate(context.Background())
	reporter.emitUpdate(context.Background())
	if got := strings.Count(out.String(), "admission:"); got != 1 {
		t.Fatalf("free/ETA wiggles emitted %d lines, want 1:\n%s", got, out.String())
	}
	reporter.emitUpdate(context.Background())
	reporter.emitUpdate(context.Background())
	if got := strings.Count(out.String(), "admission:"); got != 3 {
		t.Fatalf("blocking and holder changes emitted %d lines, want 3:\n%s", got, out.String())
	}
}

func TestQueueWaitReporterKeepsQueryFailureOutOfAdmission(t *testing.T) {
	var out strings.Builder
	reporter := &queueWaitReporter{
		la: &LocalAdmission{Out: &out}, requestID: "waiting", displayID: "waiting",
		request: wingwire.AdmissionRequest{RunID: "waiting", Resources: wingwire.HostResources{Cores: 2}},
		seen:    true, since: time.Now(), latest: wingwire.Queued{Position: 2, QueueLength: 3},
		snapshot: func(context.Context) (wingwire.QueueState, error) {
			return wingwire.QueueState{}, errors.New("fixture query failure")
		},
	}
	reporter.emitUpdate(context.Background())
	if got := out.String(); !strings.Contains(got, "machine state unavailable") || !strings.Contains(got, "position 2 of 3") {
		t.Fatalf("query failure did not produce bounded reporting fallback: %s", got)
	}
}

func TestQueueWaitReporterCancellationStopsSnapshotOutput(t *testing.T) {
	var out strings.Builder
	started := make(chan struct{})
	reporter := &queueWaitReporter{
		la: &LocalAdmission{Out: &out}, requestID: "waiting", displayID: "waiting",
		request: wingwire.AdmissionRequest{RunID: "waiting"},
		snapshot: func(ctx context.Context) (wingwire.QueueState, error) {
			close(started)
			<-ctx.Done()
			return wingwire.QueueState{Waiters: []wingwire.Waiter{{RunID: "waiting", Position: 1}}}, nil
		},
	}
	stop := reporter.startHeartbeat(context.Background())
	reporter.onQueued(wingwire.Queued{Position: 1, QueueLength: 1})
	<-started
	stop()
	if out.Len() != 0 {
		t.Fatalf("reporter emitted after its admission lifecycle stopped: %s", out.String())
	}
}

func TestQueueWaitReporterDoesNotReportAWaiterThatAlreadyLeft(t *testing.T) {
	var out strings.Builder
	reporter := &queueWaitReporter{
		la: &LocalAdmission{Out: &out}, requestID: "waiting", displayID: "waiting",
		request: wingwire.AdmissionRequest{RunID: "waiting"},
		seen:    true, since: time.Now(), latest: wingwire.Queued{Position: 1, QueueLength: 1},
		snapshot: func(context.Context) (wingwire.QueueState, error) {
			return wingwire.QueueState{}, nil
		},
	}
	reporter.emitUpdate(context.Background())
	if out.Len() != 0 {
		t.Fatalf("reporter emitted stale queue state after grant or cancellation: %s", out.String())
	}
}

func TestQueueWaitReporterUsesOneCurrentSnapshot(t *testing.T) {
	var out strings.Builder
	reporter := &queueWaitReporter{
		la: &LocalAdmission{Out: &out}, requestID: "waiting", displayID: "waiting",
		request: wingwire.AdmissionRequest{RunID: "waiting", Resources: wingwire.HostResources{Cores: 2}},
		seen:    true, since: time.Now(), latest: wingwire.Queued{Position: 3, QueueLength: 5},
		snapshot: func(context.Context) (wingwire.QueueState, error) {
			return wingwire.QueueState{
				Waiters: []wingwire.Waiter{
					{RunID: "waiting", Position: 1, WaitingOn: []string{"cores"}},
					{RunID: "other", Position: 2},
				},
			}, nil
		},
	}
	reporter.emitUpdate(context.Background())
	got := out.String()
	for _, want := range []string{"2 queued", "you are next"} {
		if !strings.Contains(got, want) {
			t.Fatalf("current snapshot omitted %q: %s", want, got)
		}
	}
	for _, stale := range []string{"5 queued", "position 3 of 5"} {
		if strings.Contains(got, stale) {
			t.Fatalf("mixed stale callback data %q into current snapshot: %s", stale, got)
		}
	}
}

func TestQueueWaitReportDelayEscalatesFromExistingCadence(t *testing.T) {
	base := 30 * time.Second
	want := []time.Duration{30 * time.Second, 30 * time.Second, time.Minute, 3 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, expected := range want {
		if got := queueWaitReportDelay(base, i); got != expected {
			t.Errorf("delay %d = %s, want %s", i, got, expected)
		}
	}
}

func queueReporterState(free, external float64, waitingOn []string, clear *int64, pipeline string) wingwire.QueueState {
	return wingwire.QueueState{
		Resources: []wingwire.ResourceState{{
			Key: "cores", Capacity: 16, Held: 6.6, Reserved: 1,
			External: external, ExternalSource: wingwire.ExternalMeasured, Available: free,
		}},
		Holders:         []wingwire.Holder{{RunID: "holder", Pipeline: pipeline, Resources: wingwire.HostResources{Cores: 5.2}}},
		Waiters:         []wingwire.Waiter{{RunID: "waiting", Position: 1, WaitingOn: waitingOn}},
		ExpectedClearMS: clear,
	}
}
