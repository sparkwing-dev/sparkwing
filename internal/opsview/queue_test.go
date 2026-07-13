package opsview_test

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestRenderQueuePretty_ResourceRowReconciles renders a host resource row and
// asserts the printed numbers satisfy capacity - in use - reserved - external
// = available exactly, and that the legend and the Running/Waiting section
// headers frame the tables.
func TestRenderQueuePretty_ResourceRowReconciles(t *testing.T) {
	qs := wingwire.QueueState{
		Resources: []wingwire.ResourceState{
			{Key: "cores", Capacity: 10, Held: 0, Reserved: 2, External: 4.07, Available: 3.93},
		},
		Holders: []wingwire.Holder{{RunID: "run-a", Resources: wingwire.HostResources{Cores: 1}}},
		Waiters: []wingwire.Waiter{{RunID: "run-b", Position: 1, Resources: wingwire.HostResources{Cores: 5}}},
	}
	var buf bytes.Buffer
	if err := opsview.RenderQueuePretty(&buf, qs); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "available = capacity - in use - reserved") {
		t.Fatalf("missing legend line:\n%s", out)
	}
	if !strings.Contains(out, "\nRunning\n") || !strings.Contains(out, "\nWaiting\n") {
		t.Fatalf("missing Running/Waiting section headers:\n%s", out)
	}
	cap, held, reserved, external, available, ok := parseCoresRow(out)
	if !ok {
		t.Fatalf("no cores row parsed from:\n%s", out)
	}
	if got := cap - held - reserved - external; math.Abs(got-available) > 1e-9 {
		t.Fatalf("row does not reconcile: %v - %v - %v - %v = %v, printed available %v",
			cap, held, reserved, external, got, available)
	}
}

func parseCoresRow(out string) (cap, held, reserved, external, available float64, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 6 || fields[0] != "cores" {
			continue
		}
		nums := make([]float64, 5)
		for i := 0; i < 5; i++ {
			v, err := strconv.ParseFloat(fields[i+1], 64)
			if err != nil {
				return 0, 0, 0, 0, 0, false
			}
			nums[i] = v
		}
		return nums[0], nums[1], nums[2], nums[3], nums[4], true
	}
	return 0, 0, 0, 0, 0, false
}

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
