package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type fullMetricsState struct {
	StateBackend
	kinds    []string
	payloads [][]byte
}

func (f *fullMetricsState) AddNodeMetricSample(context.Context, string, string, store.MetricSample) error {
	return store.ErrNodeMetricLimit
}

func (f *fullMetricsState) AppendEvent(_ context.Context, _, _, kind string, payload []byte) error {
	f.kinds = append(f.kinds, kind)
	f.payloads = append(f.payloads, payload)
	return nil
}

func TestMetricsSinkRecordsWhereSamplingStopped(t *testing.T) {
	state := &fullMetricsState{}
	sink := stateMetricsSink{backend: state, runID: "run-1", nodeID: "build"}
	err := sink.Push(context.Background(), nodemetrics.Sample{TS: time.Unix(1, 0)})
	if !errors.Is(err, nodemetrics.ErrSinkFull) {
		t.Fatalf("Push at the cap = %v, want ErrSinkFull", err)
	}
	if len(state.kinds) != 1 || state.kinds[0] != store.EventKindMetricsStopped {
		t.Fatalf("events = %v, want one %s", state.kinds, store.EventKindMetricsStopped)
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(state.payloads[0], &body); err != nil || body.Message != "metric sampling stopped at 10000 samples" {
		t.Fatalf("event payload = %s, %v", state.payloads[0], err)
	}
}
