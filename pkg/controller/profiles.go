package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// handleGetPipelineProfile serves the measured profile a cluster runner
// reads to size a node's pod: the (pipeline, node) rollup when node is
// empty, else the per-node profile. 404 when nothing has been measured yet
// so the runner falls back to its pin or a conservative default.
func (s *Server) handleGetPipelineProfile(w http.ResponseWriter, r *http.Request) {
	pipeline := r.PathValue("name")
	nodeID := r.URL.Query().Get("node")
	prof, err := s.store.GetPipelineProfile(r.Context(), pipeline, nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if prof == nil {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, prof)
}

// setPinReq is the body of a pin report: the explicit .Resources() charge
// a cluster runner applied to a node's pod, so the controller can judge it
// against measured peaks.
type setPinReq struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
}

// handleSetPipelinePin records the pin a cluster runner applied for a
// (pipeline, node). It upserts, so a runner can report what it applied
// before any run of the pipeline has been profiled.
func (s *Server) handleSetPipelinePin(w http.ResponseWriter, r *http.Request) {
	pipeline := r.PathValue("name")
	nodeID := r.URL.Query().Get("node")
	var body setPinReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var err error
	if body.Cores <= 0 && body.MemoryBytes <= 0 {
		err = s.store.SetProfilePin(r.Context(), pipeline, nodeID, 0, 0)
	} else {
		err = s.store.UpsertProfilePin(r.Context(), pipeline, nodeID, body.Cores, body.MemoryBytes)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// foldRunProfiles folds a finished cluster run's measured node metrics into
// the pipeline's stored profiles -- a per-node row plus the pipeline-level
// rollup admission and pod sizing read -- and, for any node whose applied
// pin has drifted from its measured peak, appends a resource_pin_drift
// event the run and dashboard views surface. It mirrors the local daemon's
// end-of-run profiling so one .Resources() declaration is judged the same
// in both modes. Best-effort: a profiling error never fails the run finish.
func (s *Server) foldRunProfiles(ctx context.Context, run *store.Run) {
	if run == nil || run.Pipeline == "" {
		return
	}
	nodes, err := s.store.ListNodes(ctx, run.ID)
	if err != nil {
		return
	}
	var runPeakCores float64
	var runPeakMem int64
	measured := false
	for _, n := range nodes {
		samples, err := s.store.ListNodeMetrics(ctx, run.ID, n.NodeID)
		if err != nil || len(samples) == 0 {
			continue
		}
		measured = true
		peakCores, peakMem := samplePeaks(samples)
		_ = s.store.RecordProfileObservation(ctx, run.Pipeline, n.NodeID, store.ProfileObservation{
			Duration:        nodeMetricSpan(samples),
			PeakCores:       peakCores,
			PeakMemoryBytes: peakMem,
			CPUMeasured:     true,
		})
		s.emitNodeDrift(ctx, run, n.NodeID)
		runPeakCores = maxF(runPeakCores, peakCores)
		if peakMem > runPeakMem {
			runPeakMem = peakMem
		}
	}
	if !measured {
		return
	}
	_ = s.store.RecordProfileObservation(ctx, run.Pipeline, "", store.ProfileObservation{
		Duration:        runDuration(run),
		PeakCores:       runPeakCores,
		PeakMemoryBytes: runPeakMem,
		CPUMeasured:     true,
	})
	s.emitNodeDrift(ctx, run, "")
}

// emitNodeDrift compares a (pipeline, node) profile's applied pin against
// its measured peaks and, when they have drifted past the threshold,
// records a resource_pin_drift event on the run naming the exact fix. It is
// a no-op for an unpinned node or one without enough samples to judge.
func (s *Server) emitNodeDrift(ctx context.Context, run *store.Run, nodeID string) {
	prof, err := s.store.GetPipelineProfile(ctx, run.Pipeline, nodeID)
	if err != nil || prof == nil {
		return
	}
	pin := &capacity.Pin{Cores: prof.PinnedCores, MemoryBytes: prof.PinnedMemoryBytes}
	drift := capacity.CheckDrift(pin, prof)
	if drift == nil {
		return
	}
	payload, err := json.Marshal(drift)
	if err != nil {
		return
	}
	_, _ = s.store.AppendEvent(ctx, run.ID, nodeID, "resource_pin_drift", payload)
}

// samplePeaks returns the peak cores and memory across a node's metric
// samples.
func samplePeaks(samples []store.MetricSample) (float64, int64) {
	var cores float64
	var mem int64
	for _, s := range samples {
		cores = maxF(cores, float64(s.CPUMillicores)/1000.0)
		if s.MemoryBytes > mem {
			mem = s.MemoryBytes
		}
	}
	return cores, mem
}

// nodeMetricSpan is the wall time a node's metric samples cover, used as
// the node's measured duration when the node row carries no explicit
// start/finish.
func nodeMetricSpan(samples []store.MetricSample) (d time.Duration) {
	if len(samples) < 2 {
		return 0
	}
	return samples[len(samples)-1].TS.Sub(samples[0].TS)
}

// runDuration is a finished run's wall time, floored at zero.
func runDuration(run *store.Run) time.Duration {
	if run.FinishedAt == nil {
		return 0
	}
	d := run.FinishedAt.Sub(run.StartedAt)
	if d < 0 {
		return 0
	}
	return d
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
