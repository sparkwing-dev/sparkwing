package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
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
