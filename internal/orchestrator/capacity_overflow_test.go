package orchestrator

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type profileCapture struct {
	RunCoordination
	nodes        []*store.Node
	samples      map[string][]store.MetricSample
	observations map[string]store.ProfileObservation
	pin          capacity.Pin
}

func (s *profileCapture) ListNodes(context.Context, string) ([]*store.Node, error) {
	return s.nodes, nil
}

func (s *profileCapture) ListNodeMetrics(_ context.Context, _, node string) ([]store.MetricSample, error) {
	return s.samples[node], nil
}

func (s *profileCapture) RecordProfileObservation(_ context.Context, _, node string, observation store.ProfileObservation) error {
	s.observations[node] = observation
	return nil
}

func (s *profileCapture) SetPipelinePin(_ context.Context, _, _ string, cores float64, memory int64) error {
	s.pin = capacity.Pin{Cores: cores, MemoryBytes: memory}
	return nil
}

func TestRecordRunProfile_RejectsOverflow(t *testing.T) {
	for _, dimension := range []string{"interval CPU", "interval memory", "command memory", "combined memory", "lifetime CPU"} {
		for _, valid := range []bool{false, true} {
			for _, contended := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/valid=%v/contended=%v", dimension, valid, contended), func(t *testing.T) {
					values := []int64{math.MaxInt64, math.MaxInt64, math.MaxInt64}
					if dimension == "combined memory" {
						values = []int64{math.MaxInt64 - 1, 1, 1}
					}
					if valid {
						values = []int64{math.MaxInt64 - 2, 1, 1}
					}
					start := time.Unix(1700000000, 0)
					end := start.Add(time.Second)
					st := &profileCapture{samples: map[string][]store.MetricSample{}, observations: map[string]store.ProfileObservation{}}
					for i, value := range values {
						node := &store.Node{NodeID: fmt.Sprint(i), Outcome: "success", StartedAt: &start, FinishedAt: &end, CPUNanos: 1, ProcessWallNanos: int64(time.Second)}
						sample := store.MetricSample{TS: start, CPUMillicores: 1}
						switch dimension {
						case "interval CPU":
							sample.CPUMillicores = value
						case "interval memory":
							sample.MemoryBytes = value
						case "command memory":
							sample.MemoryBytes, sample.CPUTime = value, 1
						case "combined memory":
							sample.MemoryBytes = value
							if i > 0 {
								sample.CPUTime = 1
							}
						case "lifetime CPU":
							node.CPUNanos = value
						}
						st.nodes = append(st.nodes, node)
						st.samples[node.NodeID] = []store.MetricSample{sample}
					}
					pin := capacity.Pin{Cores: 2, MemoryBytes: 512}
					recordRunProfile(t.Context(), st, "demo", "run", &pin, "shape", runCharge{}, contended, start, end)
					if _, recorded := st.observations[""]; recorded != valid {
						t.Errorf("run recorded = %v, want %v; observation %+v", recorded, valid, st.observations[""])
					}
					if st.pin != pin {
						t.Errorf("pin = %+v, want %+v", st.pin, pin)
					}
					for _, node := range st.nodes {
						if _, recorded := st.observations[node.NodeID]; recorded == contended {
							t.Errorf("node %s recorded = %v, contended = %v", node.NodeID, recorded, contended)
						}
					}
				})
			}
		}
	}
}
