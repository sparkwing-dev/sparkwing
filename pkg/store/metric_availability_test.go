package store_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMetricAvailabilityRoundTrip(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	start := time.Unix(100, 0).UTC()
	if err := st.CreateRun(ctx, store.Run{ID: "r", Pipeline: "parcels", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r", NodeID: "sort", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	samples := []store.MetricSample{
		{Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true},
		{Kind: store.MetricInterval, MemoryAvailable: true, MemoryBytes: 4096},
		{Kind: store.MetricInterval, CPUAvailable: true, CPUMillicores: 500},
		{Kind: store.MetricCommand, CPUAvailable: true, MemoryAvailable: true, MemoryBytes: 8192},
		{CPUMillicores: 700, MemoryBytes: 1024},
	}
	for i := range samples {
		samples[i].TS = start.Add(time.Duration(i) * time.Second)
		if err := st.AddNodeMetricSample(ctx, "r", "sort", samples[i]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListNodeMetrics(ctx, "r", "sort")
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		got[i].TS = got[i].TS.UTC()
	}
	if !reflect.DeepEqual(got, samples) {
		t.Fatalf("sample metadata changed: got %+v, want %+v", got, samples)
	}
	if !got[3].OneShot() {
		t.Fatal("zero-CPU command lost its command kind")
	}
	if got[4].CPUAvailable || got[4].MemoryAvailable {
		t.Fatal("historical sample was promoted to available")
	}
}

func TestMetricAvailabilityRejectsInvalidInput(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	for _, sample := range []store.MetricSample{
		{Kind: "bogus"},
		{CPUAvailable: true},
		{MemoryAvailable: true},
		{Kind: store.MetricInterval, CPUTime: time.Second},
		{Kind: store.MetricCommand, CPUTime: -1},
		{Kind: store.MetricInterval, CPUMillicores: -1},
		{Kind: store.MetricInterval, MemoryBytes: -1},
	} {
		if err := sample.Validate(); err == nil {
			t.Errorf("invalid metric accepted: %+v", sample)
		}
		if err := st.AddNodeMetricSample(ctx, "absent", "absent", sample); err == nil {
			t.Errorf("invalid metric persisted: %+v", sample)
		}
	}
}
