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

func TestRenderQueuePretty_UsesDisplayRunID(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{
			{
				RunID:         "run-1",
				ParticipantID: "internal-holder",
				DisplayRunID:  "run-1/build",
				Resources:     wingwire.HostResources{Cores: 1},
			},
		},
		Waiters: []wingwire.Waiter{
			{
				RunID:          "run-2",
				ParticipantID:  "internal-waiter",
				DisplayRunID:   "run-2/test",
				Position:       1,
				Priority:       50,
				Resources:      wingwire.HostResources{Cores: 1},
				BlockingReason: "needs 1.0 cores; 0.0 available",
			},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderQueuePretty(&buf, qs); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"run-1/build", "run-2/test"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pretty queue omitted %q:\n%s", want, out)
		}
	}
	for _, internal := range []string{"internal-holder", "internal-waiter"} {
		if strings.Contains(out, internal) {
			t.Fatalf("pretty queue leaked participant id %q:\n%s", internal, out)
		}
	}
	if !strings.Contains(out, "POS  PRI") || !strings.Contains(out, "1    50") {
		t.Fatalf("pretty queue omitted priority column:\n%s", out)
	}
}

func TestRenderQueuePretty_ShowsAdmissionWaitingRunOnce(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{
			{
				RunID:                      "run-1",
				ElapsedMS:                  90_000,
				Semaphores:                 []string{"gate"},
				Stalled:                    true,
				Recovery:                   "sparkwing runs cancel --run run-1",
				AdmissionWaiting:           true,
				ActiveWaiterParticipantIDs: []string{"run-1/node-host/YnVpbGQ"},
			},
		},
		Waiters: []wingwire.Waiter{
			{
				RunID:          "run-1",
				ParticipantID:  "run-1/node-host/YnVpbGQ",
				DisplayRunID:   "run-1/build",
				Position:       1,
				WaitingMS:      30_000,
				Resources:      wingwire.HostResources{Cores: 6},
				WaitingOn:      []string{"cores"},
				BlockingReason: "needs 6.0 cores; 1.3 available",
			},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderQueuePretty(&buf, qs); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"0 holding, 0 connected, 1 queued", "run-1/build", "cores", "30s", "needs 6.0 cores; 1.3 available"} {
		if !strings.Contains(out, want) {
			t.Fatalf("admission wait omitted %q:\n%s", want, out)
		}
	}
	for _, absent := range []string{"(stalled)", "sparkwing runs cancel --run run-1"} {
		if strings.Contains(out, absent) {
			t.Fatalf("admission wait rendered contradictory %q:\n%s", absent, out)
		}
	}
}

func TestRenderQueuePretty_ShowsConnectedRunWhileItsNodeWaits(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{
			{
				RunID:                      "run-connected",
				Pipeline:                   "pre-push",
				ConnectionOnly:             true,
				AdmissionWaiting:           true,
				ActiveWaiterParticipantIDs: []string{"run-connected/node-host/dGVzdA"},
			},
		},
		Waiters: []wingwire.Waiter{
			{
				RunID:         "run-connected",
				ParticipantID: "run-connected/node-host/dGVzdA",
				DisplayRunID:  "run-connected/test",
				Position:      1,
				Resources:     wingwire.HostResources{Cores: 2},
				WaitingOn:     []string{"cores"},
			},
		},
	}

	var buf bytes.Buffer
	if err := opsview.RenderQueuePretty(&buf, qs); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"0 holding, 1 connected, 1 queued",
		"Connected (no resources held)",
		"run-connected",
		"run-connected/test",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("connected admission wait omitted %q:\n%s", want, out)
		}
	}
}

func TestRenderQueuePlain_IncludesParticipantAndDisplayIdentity(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{
			{
				RunID:         "run-1",
				ParticipantID: "internal-holder",
				DisplayRunID:  "run-1/build",
				Resources:     wingwire.HostResources{Cores: 1},
			},
		},
		Waiters: []wingwire.Waiter{
			{
				RunID:         "run-1",
				ParticipantID: "internal-waiter",
				DisplayRunID:  "run-1/test",
				Position:      1,
				Priority:      50,
				Resources:     wingwire.HostResources{Cores: 1},
			},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderQueue(&buf, qs, "plain"); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"holder\trun-1\tinternal-holder\trun-1/build",
		"waiter\t1\trun-1\tinternal-waiter\trun-1/test",
		"\t50\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plain queue omitted %q:\n%s", want, out)
		}
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
		Holders: []wingwire.Holder{{
			RunID: "run-a", ElapsedMS: 1000, AdmissionWaiting: true,
			ConnectionOnly:             true,
			ActiveWaiterParticipantIDs: []string{"run-a/node-host/dGVzdA"},
		}},
		Waiters: []wingwire.Waiter{{RunID: "run-b", Position: 1, Priority: 25}},
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
	if len(got.Waiters) != 1 || got.Waiters[0].Priority != 25 {
		t.Fatalf("round-trip lost waiter priority: %+v", got.Waiters)
	}
	if !got.Holders[0].AdmissionWaiting || len(got.Holders[0].ActiveWaiterParticipantIDs) != 1 {
		t.Fatalf("round-trip lost admission-wait hierarchy: %+v", got.Holders[0])
	}
	if !got.Holders[0].ConnectionOnly {
		t.Fatalf("round-trip lost connection-only distinction: %+v", got.Holders[0])
	}
}

func TestRenderQueue_SeparatesConnectionsFromResourceHolders(t *testing.T) {
	qs := wingwire.QueueState{Holders: []wingwire.Holder{
		{RunID: "connected-run", Pipeline: "pre-commit", ConnectionOnly: true},
		{RunID: "holding-run", Pipeline: "checks", Resources: wingwire.HostResources{Cores: 1}},
	}}

	var pretty bytes.Buffer
	if err := opsview.RenderQueuePretty(&pretty, qs); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := pretty.String()
	for _, want := range []string{"1 holding, 1 connected, 0 queued", "Connected (no resources held)", "connected-run", "holding-run"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pretty queue omitted %q:\n%s", want, out)
		}
	}

	var plain bytes.Buffer
	if err := opsview.RenderQueue(&plain, qs, "plain"); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	if !strings.Contains(plain.String(), "connected\tconnected-run") || !strings.Contains(plain.String(), "holder\tholding-run") {
		t.Fatalf("plain queue did not distinguish connection from holder:\n%s", plain.String())
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
