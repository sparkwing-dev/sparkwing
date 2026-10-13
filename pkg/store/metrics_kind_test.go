package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMetricKindsRoundTrip(t *testing.T) {
	st := storetest.New(t).Open(t)
	readyNode(t, st, "kind-run", "node")
	samples := []store.MetricSample{
		{Kind: store.MetricInterval, TS: time.Unix(1, 0), MemoryBytes: 200},
		{Kind: store.MetricCommand, TS: time.Unix(2, 0), MemoryBytes: 400},
		{Kind: store.MetricUnknown, TS: time.Unix(3, 0), MemoryBytes: 600},
		{Kind: store.MetricCommand, TS: time.Unix(4, 0), CPUTime: time.Second, CPUMillicores: 500},
	}
	for _, sample := range samples {
		if err := st.AddNodeMetricSample(t.Context(), "kind-run", "node", sample); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListNodeMetrics(t.Context(), "kind-run", "node")
	if err != nil || len(got) != len(samples) {
		t.Fatalf("samples = %v, %v", got, err)
	}
	for i, want := range samples {
		if got[i].Kind != want.Kind || got[i].CPUTime != want.CPUTime || got[i].MemoryBytes != want.MemoryBytes || got[i].CPUMillicores != want.CPUMillicores || !got[i].TS.Equal(want.TS) {
			t.Fatalf("sample %d = %+v; want %+v", i, got[i], want)
		}
	}
}

func TestMetricKindsRejectInvalidCombinations(t *testing.T) {
	st := storetest.New(t).Open(t)
	readyNode(t, st, "kind-run", "node")
	for _, sample := range []store.MetricSample{
		{Kind: "future", TS: time.Unix(1, 0)},
		{Kind: store.MetricInterval, CPUTime: time.Second, TS: time.Unix(2, 0)},
	} {
		if err := st.AddNodeMetricSample(t.Context(), "kind-run", "node", sample); err == nil {
			t.Fatalf("accepted %+v", sample)
		}
	}
	got, err := st.ListNodeMetrics(t.Context(), "kind-run", "node")
	if err != nil || len(got) != 0 {
		t.Fatalf("invalid samples persisted: %v, %v", got, err)
	}
}
