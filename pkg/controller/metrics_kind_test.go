package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSamplePeaksExcludeCommandCPU(t *testing.T) {
	for _, tc := range []struct {
		name     string
		samples  []store.MetricSample
		cores    float64
		memory   int64
		measured bool
	}{
		{"command", []store.MetricSample{{Kind: store.MetricCommand, CPUMillicores: 9000, CPUTime: 9 * time.Second, MemoryBytes: 400}}, 0, 400, false},
		{"interval-and-command", []store.MetricSample{{Kind: store.MetricInterval, CPUMillicores: 100, MemoryBytes: 200}, {Kind: store.MetricCommand, CPUMillicores: 9000, CPUTime: 9 * time.Second, MemoryBytes: 400}}, 0.1, 400, true},
		{"zero-command", []store.MetricSample{{Kind: store.MetricCommand, MemoryBytes: 400}}, 0, 400, false},
		{"zero-interval", []store.MetricSample{{Kind: store.MetricInterval, MemoryBytes: 200}}, 0, 200, true},
		{"mixed-unknown", []store.MetricSample{{Kind: store.MetricInterval, CPUMillicores: 100}, {CPUMillicores: 9000, MemoryBytes: 400}}, 0.1, 400, false},
		{"unknown", []store.MetricSample{{CPUMillicores: 9000, MemoryBytes: 400}}, 0, 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cores, memory, measured := samplePeaks(tc.samples)
			if cores != tc.cores || memory != tc.memory || measured != tc.measured {
				t.Fatalf("peaks = %v cores, %d bytes, measured=%v; want %v cores, %d bytes, measured=%v", cores, memory, measured, tc.cores, tc.memory, tc.measured)
			}
		})
	}
}

func (s metricState) ListNodeMetrics(ctx context.Context, run, node string) ([]store.MetricSample, error) {
	return s.store.ListNodeMetrics(ctx, run, node)
}

func TestMetricKindLoopbackRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateRun(t.Context(), store.Run{ID: "run", Pipeline: "sample", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(t.Context(), store.Node{RunID: "run", NodeID: "node", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	loopback := &Loopback{state: metricState{store: st}}
	for _, body := range []string{
		`{"ts":"2026-01-01T00:00:01Z","kind":"interval","memory_bytes":200}`,
		`{"ts":"2026-01-01T00:00:02Z","kind":"command","memory_bytes":400}`,
		`{"ts":"2026-01-01T00:00:03Z","memory_bytes":600}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", "run")
		req.SetPathValue("nodeID", "node")
		response := httptest.NewRecorder()
		loopback.handleAddNodeMetric(response, req)
		if response.Code != http.StatusNoContent {
			t.Fatalf("POST=%d: %s", response.Code, response.Body)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("id", "run")
	req.SetPathValue("nodeID", "node")
	response := httptest.NewRecorder()
	loopback.handleGetNodeMetrics(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("GET=%d: %s", response.Code, response.Body)
	}
	var body struct {
		Points []struct {
			Kind    string `json:"kind"`
			CPU     int64  `json:"cpu_millicores"`
			Memory  int64  `json:"memory_bytes"`
			CPUTime int64  `json:"cpu_time_nanos"`
		} `json:"points"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Points) != 3 {
		t.Fatalf("points=%+v", body.Points)
	}
	for i, want := range []string{"interval", "command", ""} {
		got := body.Points[i]
		if got.Kind != want || got.CPU != 0 || got.CPUTime != 0 || got.Memory != int64((i+1)*200) {
			t.Fatalf("point%d=%+v; want kind%q and zero CPU", i, got, want)
		}
	}
}

func TestMetricKindRejectedByOlderRequestShape(t *testing.T) {
	var previous struct {
		TS            string `json:"ts"`
		CPUMillicores int64  `json:"cpu_millicores"`
		MemoryBytes   int64  `json:"memory_bytes"`
		CPUTimeNanos  int64  `json:"cpu_time_nanos,omitempty"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"kind":"command","cpu_time_nanos":0}`))
	req.Header.Set("Content-Type", "application/json")
	if err := decodeJSON(req, &previous); err == nil || !strings.Contains(err.Error(), `unknown field "kind"`) {
		t.Fatalf("older request shape accepted kind: %v", err)
	}
}
