package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestMetricAndUsageClientsUseFinalJobRoutes(t *testing.T) {
	calls := 0
	stamp := time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)
	transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		leaf := "metrics"
		if calls == 2 {
			leaf = "usage"
		}
		if r.URL.EscapedPath() != "/api/v1/runs/run%2Fone/jobs/job%2Fone/"+leaf || r.Header.Get("Authorization") != "Bearer private-bearer" {
			t.Fatalf("request=%s %s", r.Method, r.URL)
		}
		status, body := 204, ""
		if calls < 3 {
			if r.Method != "POST" {
				t.Fatalf("write method=%s", r.Method)
			}
			var value map[string]any
			if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
				t.Fatal(err)
			}
			if value["cpu_time_nanos"] != float64(time.Second) {
				t.Fatalf("CPU accounting=%v", value)
			}
			if calls == 1 && (value["kind"] != "command" || value["ts"] != "2026-10-15T01:02:03.000000000Z") {
				t.Fatalf("metric=%v", value)
			}
		} else {
			if r.Method != "GET" || r.URL.Query().Get("limit") == "" {
				t.Fatalf("read=%s %s", r.Method, r.URL)
			}
			status, body = 200, `{"points":[{"kind":"command","ts":"2026-10-15T01:02:03Z","cpu_time_nanos":1000000000,"memory_bytes":3}]}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	c := NewWithToken("http://example.invalid", &http.Client{Transport: transport}, "private-bearer")
	if err := c.AddNodeMetricSample(t.Context(), "run/one", "job/one", store.MetricSample{Kind: store.MetricCommand, TS: stamp, CPUTime: time.Second, MemoryBytes: 3}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddNodeUsage(t.Context(), "run/one", "job/one", store.NodeUsage{CPUTime: time.Second}); err != nil {
		t.Fatal(err)
	}
	samples, err := c.ListNodeMetrics(t.Context(), "run/one", "job/one")
	if err != nil || len(samples) != 1 || samples[0].Kind != store.MetricCommand || samples[0].CPUTime != time.Second || !samples[0].TS.Equal(stamp) || calls != 3 {
		t.Fatalf("samples=%v err=%v calls=%d", samples, err, calls)
	}
}
