package opsview_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestRenderQueue_JSONRoundTrips(t *testing.T) {
	want := wingwire.QueueState{
		DaemonVersion: "v9.9.9",
		Resources:     []wingwire.ResourceState{{Key: "cores", Capacity: 8, Held: 2, Available: 6}},
		Holders:       []wingwire.Holder{{RunID: "run-a", ElapsedMS: 1000}},
	}
	var buf bytes.Buffer
	if err := opsview.RenderQueue(&buf, want, "json"); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var got wingwire.QueueState
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json is not a QueueState: %v", err)
	}
	if got.DaemonVersion != "v9.9.9" || len(got.Holders) != 1 || got.Holders[0].RunID != "run-a" {
		t.Fatalf("round-trip lost fields: %+v", got)
	}
}

func TestRenderQueuePretty_ShowsCapacityChangeAndRunners(t *testing.T) {
	qs := wingwire.QueueState{
		CapacityChange: &wingwire.CapacityChange{FromCores: 4, ToCores: 8},
		Runners:        []wingwire.RunnerHeadroom{{Name: "host-7", Cores: 3.5, QueueDepth: 2}},
	}
	var buf bytes.Buffer
	if err := opsview.RenderQueuePretty(&buf, qs); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "capacity changed: 4 -> 8 cores") {
		t.Fatalf("pretty view omits the capacity-change header:\n%s", out)
	}
	if !strings.Contains(out, "host-7") || !strings.Contains(out, "RUNNER") {
		t.Fatalf("pretty view omits the runner headroom table:\n%s", out)
	}
}

func TestRenderStats_EmptyWindow(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderStats(&buf, wingwire.QueueState{}, "pretty"); err != nil {
		t.Fatalf("render stats: %v", err)
	}
	if !strings.Contains(buf.String(), "no admission activity recorded") {
		t.Fatalf("empty stats view: %q", buf.String())
	}
}
