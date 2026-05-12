package orchestrator

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBuildTimelineRows_NodesOnlyByDefault(t *testing.T) {
	start := ts("2026-05-12T12:00:00Z")
	mid := start.Add(2 * time.Second)
	end := start.Add(4 * time.Second)
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", StartedAt: &start, FinishedAt: &mid},
		{NodeID: "deploy", Status: "done", StartedAt: &mid, FinishedAt: &end},
	}
	steps := []*store.NodeStep{
		{NodeID: "build", StepID: "compile", StartedAt: &start, FinishedAt: &mid},
	}
	rows := buildTimelineRows(start, end.Sub(start), nodes, steps, false)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Kind != "node" {
			t.Errorf("unexpected kind %q", r.Kind)
		}
	}
}

func TestBuildTimelineRows_StepsIncluded(t *testing.T) {
	start := ts("2026-05-12T12:00:00Z")
	mid := start.Add(2 * time.Second)
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", StartedAt: &start, FinishedAt: &mid},
	}
	steps := []*store.NodeStep{
		{NodeID: "build", StepID: "compile", StartedAt: &start, FinishedAt: &mid},
	}
	rows := buildTimelineRows(start, mid.Sub(start), nodes, steps, true)
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].Kind != "node" || rows[1].Kind != "step" {
		t.Errorf("rows order wrong: %+v", rows)
	}
	if rows[1].StepID != "compile" {
		t.Errorf("step id wrong: %+v", rows[1])
	}
}

func TestOffsetWindow_NilStartedYieldsZero(t *testing.T) {
	start := ts("2026-05-12T12:00:00Z")
	span := 10 * time.Second
	finished := start.Add(5 * time.Second)
	s, e := offsetWindow(start, span, nil, &finished)
	if s != 0 || e != 5000 {
		t.Errorf("got s=%d e=%d, want 0 5000", s, e)
	}
}

func TestOffsetWindow_NilFinishedUsesSpan(t *testing.T) {
	start := ts("2026-05-12T12:00:00Z")
	span := 10 * time.Second
	started := start.Add(2 * time.Second)
	s, e := offsetWindow(start, span, &started, nil)
	if s != 2000 || e != 10000 {
		t.Errorf("got s=%d e=%d, want 2000 10000", s, e)
	}
}

func TestWaterfallBar_PlacesFillInRange(t *testing.T) {
	bar := waterfallBar(2000, 8000, 10000, 20)
	if len(bar) != 20 {
		t.Fatalf("bar len=%d", len(bar))
	}
	// columns 4..16 should be '#', the rest '.'
	for i, c := range bar {
		if i >= 4 && i < 16 {
			if c != '#' {
				t.Errorf("col %d = %q, want '#'", i, c)
			}
		} else {
			if c != '.' {
				t.Errorf("col %d = %q, want '.'", i, c)
			}
		}
	}
}

func TestWaterfallBar_NonZeroDurationGetsAtLeastOneFillChar(t *testing.T) {
	bar := waterfallBar(0, 1, 100000, 20)
	if !strings.Contains(bar, "#") {
		t.Errorf("expected at least one fill char in %q", bar)
	}
}

func TestRenderTimeline_JSONHasRows(t *testing.T) {
	start := ts("2026-05-12T12:00:00Z")
	end := start.Add(5 * time.Second)
	run := &store.Run{ID: "run-x", StartedAt: start, FinishedAt: &end}
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", StartedAt: &start, FinishedAt: &end},
	}
	var buf bytes.Buffer
	if err := renderTimeline(run, nodes, nil, TimelineOpts{JSON: true}, &buf); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		RunID      string        `json:"run_id"`
		Rows       []TimelineRow `json:"rows"`
		DurationMS int64         `json:"duration_ms"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if payload.RunID != "run-x" || len(payload.Rows) != 1 || payload.DurationMS != 5000 {
		t.Errorf("unexpected payload: %+v", payload)
	}
}

func TestRenderTimeline_TextHasBarAndRange(t *testing.T) {
	start := ts("2026-05-12T12:00:00Z")
	end := start.Add(5 * time.Second)
	run := &store.Run{ID: "run-x", StartedAt: start, FinishedAt: &end}
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", StartedAt: &start, FinishedAt: &end},
	}
	var buf bytes.Buffer
	if err := renderTimeline(run, nodes, nil, TimelineOpts{Width: 20}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "build") {
		t.Errorf("missing node label in:\n%s", out)
	}
	if !strings.Contains(out, "#") {
		t.Errorf("missing bar fill in:\n%s", out)
	}
	if !strings.Contains(out, "00:00 → 00:05") {
		t.Errorf("missing range marker in:\n%s", out)
	}
}
