package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"runtime"
	"strings"
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
	for _, dimension := range []string{"interval CPU", "interval memory", "lifetime CPU"} {
		for _, valid := range []bool{false, true} {
			for _, contended := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/valid=%v/contended=%v", dimension, valid, contended), func(t *testing.T) {
					values := []int64{math.MaxInt64, math.MaxInt64, math.MaxInt64}
					if valid {
						values = []int64{math.MaxInt64 - 2, 1, 1}
					}
					start := time.Unix(1700000000, 0)
					end := start.Add(time.Second)
					st := &profileCapture{samples: map[string][]store.MetricSample{}, observations: map[string]store.ProfileObservation{}}
					for i, value := range values {
						node := &store.Node{NodeID: fmt.Sprint(i), Status: "done", Outcome: "success", StartedAt: &start, FinishedAt: &end, CPUNanos: 1, ProcessWallNanos: int64(time.Second)}
						sample := store.MetricSample{Kind: store.MetricInterval, TS: start, CPUMillicores: 1}
						switch dimension {
						case "interval CPU":
							sample.CPUMillicores = value
						case "interval memory":
							sample.MemoryBytes = value
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
					if valid {
						observation := st.observations[""]
						cores, memory := observation.PeakCores, observation.PeakMemoryBytes
						if contended {
							cores, memory = observation.FloorCores, observation.FloorMemoryBytes
						}
						if dimension == "interval CPU" || dimension == "lifetime CPU" {
							if want := float64(runtime.NumCPU()); cores != want {
								t.Errorf("CPU charge = %v, want host limit %v", cores, want)
							}
						} else if memory != math.MaxInt64 {
							t.Errorf("memory = %d, want %d", memory, int64(math.MaxInt64))
						}
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

func TestRecordRunProfile_WarnsOnlyForOutOfRangeReadings(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	start := time.Unix(1700000000, 0)
	end := start.Add(200 * time.Millisecond)
	key := store.JoinProfileKey("repo", "feat")
	record := func(samples []store.MetricSample) string {
		logged.Reset()
		node := &store.Node{NodeID: "n", Status: "done", Outcome: "success", StartedAt: &start, FinishedAt: &end}
		st := &profileCapture{
			nodes:        []*store.Node{node},
			samples:      map[string][]store.MetricSample{"n": samples},
			observations: map[string]store.ProfileObservation{},
		}
		recordRunProfile(t.Context(), st, key, "run", &capacity.Pin{}, "shape", runCharge{}, false, start, end)
		return logged.String()
	}

	if out := record(nil); strings.Contains(out, "measurements") {
		t.Errorf("a sub-second run with no samples warned:\n%s", out)
	}
	out := record([]store.MetricSample{{Kind: store.MetricInterval, TS: start, CPUMillicores: -1}})
	if !strings.Contains(out, "exceed the supported range") || !strings.Contains(out, "pipeline=repo/feat") {
		t.Errorf("an out-of-range reading logged %q, want a warning naming repo/feat", out)
	}
}
