package s3state

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestMetricAvailabilityEnvelope(t *testing.T) {
	raw := `{"kind":"metric_sample","data":{"node_id":"sort","sample":{"Kind":"command","CPUAvailable":true,"MemoryAvailable":false,"CPUTime":0,"MemoryBytes":4096}}}`
	envs, err := parseEnvelopes(strings.NewReader(raw + "\n"))
	if err != nil || len(envs) != 1 {
		t.Fatalf("envelopes=%+v error=%v", envs, err)
	}
	state := newRunState()
	if err := applyEnvelope(state, envs[0]); err != nil {
		t.Fatal(err)
	}
	got := state.metrics["sort"]
	if len(got) != 1 {
		t.Fatalf("metrics=%+v", got)
	}
	want := store.MetricSample{Kind: store.MetricCommand, CPUAvailable: true, MemoryBytes: 4096}
	if got[0] != want {
		t.Fatalf("metadata changed: got %+v want %+v", got[0], want)
	}
}
