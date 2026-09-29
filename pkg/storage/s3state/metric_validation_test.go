package s3state_test

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestMetricWritesValidateResourceValues(t *testing.T) {
	cases := []struct {
		name   string
		sample store.MetricSample
		valid  bool
	}{
		{"JSON timestamp", store.MetricSample{TS: time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)}, true},
		{"unknown", store.MetricSample{}, true},
		{"zero interval", store.MetricSample{Kind: store.MetricInterval}, true},
		{"zero command", store.MetricSample{Kind: store.MetricCommand}, true},
		{"command", store.MetricSample{Kind: store.MetricCommand, CPUMillicores: 500, MemoryBytes: 200, CPUTime: time.Second}, true},
		{"invalid kind", store.MetricSample{Kind: "invalid"}, false},
		{"interval command time", store.MetricSample{Kind: store.MetricInterval, CPUTime: time.Second}, false},
		{"negative CPU rate", store.MetricSample{CPUMillicores: -1}, false},
		{"negative memory", store.MetricSample{MemoryBytes: -1}, false},
		{"negative CPU time", store.MetricSample{Kind: store.MetricCommand, CPUTime: -1}, false},
	}
	for _, viaHTTP := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(map[bool]string{false: "direct", true: "loopback"}[viaHTTP]+"/"+tc.name, func(t *testing.T) {
				art := newMemArt()
				b := s3state.New(art, s3state.WithFlushInterval(time.Hour))
				t.Cleanup(func() { _ = b.Close() })
				ctx := t.Context()
				if err := b.CreateRun(ctx, runningRun("run")); err != nil {
					t.Fatal(err)
				}
				if err := b.CreateNode(ctx, store.Node{RunID: "run", NodeID: "build", Status: "running"}); err != nil {
					t.Fatal(err)
				}
				submit := b.AddNodeMetricSample
				if viaHTTP {
					server := httptest.NewServer(controller.NewLoopback(b, "run", "", nil).Handler())
					t.Cleanup(server.Close)
					submit = client.New(server.URL, nil).AddNodeMetricSample
				}
				sample := tc.sample
				if sample.TS.IsZero() {
					sample.TS = time.Unix(100, 0)
				}
				err := submit(ctx, "run", "build", sample)
				if (err == nil) != tc.valid {
					t.Errorf("accepted=%t, want %t: %v", err == nil, tc.valid, err)
				}
				if err := b.FinishRun(ctx, "run", "success", ""); err != nil {
					t.Fatal(err)
				}
				reader, err := art.Get(ctx, "runs/run/state.ndjson")
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				decoder := json.NewDecoder(reader)
				count := 0
				for {
					var envelope struct {
						Kind string
						Data json.RawMessage
					}
					err := decoder.Decode(&envelope)
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if envelope.Kind == s3state.KindMetricSample {
						count++
						var got struct {
							NodeID string             `json:"node_id"`
							Sample store.MetricSample `json:"sample"`
						}
						if err := json.Unmarshal(envelope.Data, &got); err != nil {
							t.Fatal(err)
						}
						if got.NodeID != "build" || !got.Sample.TS.Equal(sample.TS) || got.Sample.Kind != sample.Kind || got.Sample.CPUMillicores != sample.CPUMillicores || got.Sample.MemoryBytes != sample.MemoryBytes || got.Sample.CPUTime != sample.CPUTime {
							t.Errorf("stored metric=%+v, want node build sample=%+v", got, sample)
						}
					}
				}
				want := 0
				if tc.valid {
					want = 1
				}
				if count != want {
					t.Errorf("persisted metric envelopes=%d, want %d", count, want)
				}
			})
		}
	}
}
