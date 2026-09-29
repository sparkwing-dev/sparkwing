package client_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestClientMetricAvailabilityRoundTrip(t *testing.T) {
	f := newCoordinationFixture(t)
	samples := []store.MetricSample{
		{Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true},
		{Kind: store.MetricInterval, MemoryAvailable: true, MemoryBytes: 4096},
		{Kind: store.MetricInterval, CPUAvailable: true, CPUMillicores: 500},
		{Kind: store.MetricCommand, CPUAvailable: true, MemoryAvailable: true, MemoryBytes: 8192},
		{CPUMillicores: 700, MemoryBytes: 1024},
	}
	for i := range samples {
		samples[i].TS = time.Unix(100+int64(i), 0).UTC()
		if err := f.runner.AddNodeMetricSample(f.ctx, "r1", "build", samples[i]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.runner.ListNodeMetrics(f.ctx, "r1", "build")
	if err != nil || len(got) != len(samples) {
		t.Fatalf("samples=%+v error=%v", got, err)
	}
	for i, want := range samples {
		got[i].TS = got[i].TS.UTC()
		if got[i] != want {
			t.Errorf("sample %d changed: got %+v want %+v", i, got[i], want)
		}
	}
}
