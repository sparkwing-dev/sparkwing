package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type countingEvents struct {
	events []store.Event
	calls  int
	err    error
}

func (c *countingEvents) ListEventsAfter(_ context.Context, _ string, after int64, limit int) ([]store.Event, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	var out []store.Event
	for _, e := range c.events {
		if e.Seq > after {
			out = append(out, e)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func excerptEvent(seq int64, nodeID string) store.Event {
	return store.Event{
		Seq: seq, NodeID: nodeID, Kind: nodeFailureExcerptEvent,
		Payload: []byte(`{"log_excerpt":"boom","log_excerpt_truncated":false}`),
	}
}

func filler(from, to int64) []store.Event {
	var out []store.Event
	for seq := from; seq <= to; seq++ {
		out = append(out, store.Event{Seq: seq, NodeID: "n", Kind: "node_started"})
	}
	return out
}

func TestFailureExcerptsFor_ScansOnlyWhenItMustAndStopsEarly(t *testing.T) {
	src := &countingEvents{events: filler(1, 5000)}
	ix := failureExcerptsFor(context.Background(), src, "run-1", nil)
	if src.calls != 0 {
		t.Fatalf("a run with no failed nodes made %d event calls, want 0", src.calls)
	}
	if ix.Incomplete || ix.Unavailable("anything") {
		t.Fatal("an empty lookup is vacuously complete")
	}

	early := append([]store.Event{excerptEvent(2, "build")}, filler(3, 5000)...)
	src = &countingEvents{events: early}
	ix = failureExcerptsFor(context.Background(), src, "run-1", map[string]struct{}{"build": {}})
	if src.calls != 1 {
		t.Fatalf("scan made %d calls after finding every excerpt in page 1, want 1", src.calls)
	}
	if ex, ok := ix.Get("build"); !ok || ex.LogExcerpt != "boom" {
		t.Fatalf("excerpt not found: %+v", ix)
	}
	if ix.Incomplete {
		t.Fatal("a scan that found everything is complete")
	}
}

func TestFailureExcerptsFor_IncompleteScanReportsUnavailable(t *testing.T) {
	want := map[string]struct{}{"build": {}}

	huge := &countingEvents{events: filler(1, excerptPageSize*excerptMaxPages+excerptPageSize)}
	ix := failureExcerptsFor(context.Background(), huge, "run-1", want)
	if !ix.Incomplete {
		t.Fatal("a scan that hit the page cap must not claim completeness")
	}
	if !ix.Unavailable("build") {
		t.Fatal("a failed node the scan never reached is excerpt-unavailable")
	}
	if huge.calls != excerptMaxPages {
		t.Fatalf("scan made %d pages, want the %d-page cap", huge.calls, excerptMaxPages)
	}

	broken := &countingEvents{err: errors.New("controller down")}
	ix = failureExcerptsFor(context.Background(), broken, "run-1", want)
	if !ix.Incomplete || !ix.Unavailable("build") {
		t.Fatal("a backend that cannot serve events yields unavailable, not absent")
	}

	quiet := &countingEvents{events: filler(1, 10)}
	ix = failureExcerptsFor(context.Background(), quiet, "run-1", want)
	if ix.Incomplete || ix.Unavailable("build") {
		t.Fatal("a completed scan reports authoritative absence")
	}
}

func TestFailedNodeReports_MarksUnavailableNotAbsent(t *testing.T) {
	nodes := []*store.Node{
		{NodeID: "build", Outcome: "failed", Error: "boom"},
		{NodeID: "deploy", Outcome: "cancelled", Error: "upstream-failed"},
	}

	rows := failedNodeReports(nodes, failureExcerptIndex{Incomplete: true})
	if len(rows) != 1 || !rows[0].LogExcerptUnavailable || rows[0].LogExcerpt != "" {
		t.Fatalf("incomplete index should mark the failure unavailable: %+v", rows)
	}
	rows = failedNodeReports(nodes, failureExcerptIndex{})
	if len(rows) != 1 || rows[0].LogExcerptUnavailable {
		t.Fatalf("complete index should report plain absence: %+v", rows)
	}

	rows = failedNodeReports(nodes, failureExcerptIndex{})
	if len(rows) != 1 || rows[0].LogExcerptUnavailable || rows[0].LogExcerpt != "" {
		t.Fatalf("the zero index must make no claim either way: %+v", rows)
	}
	zeroWrapped := withFailureExcerpts([]nodeWithSteps{{Node: nodes[0]}}, failureExcerptIndex{})
	if zeroWrapped[0].LogExcerptUnavailable {
		t.Fatalf("the zero index must not mark nodes unavailable: %+v", zeroWrapped[0])
	}

	wrapped := withFailureExcerpts([]nodeWithSteps{
		{Node: nodes[0]}, {Node: nodes[1]},
	}, failureExcerptIndex{Incomplete: true})
	if !wrapped[0].LogExcerptUnavailable {
		t.Fatalf("failed node should be marked unavailable: %+v", wrapped[0])
	}
	if wrapped[1].LogExcerptUnavailable {
		t.Fatalf("a cancelled node never had an excerpt to lose: %+v", wrapped[1])
	}
}
