package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"math"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) handleGetPipelineProfile(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.sizingReader(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	prof, err := tenant.GetPipelineProfile(r.Context(), a.Target.Pipeline, a.Target.NodeID)
	if err != nil {
		return engineLookupError(err)
	}
	if prof == nil {
		return &apierr.Error{Code: apierr.NotFound, Message: "sizing record not found"}
	}
	writeJSON(w, http.StatusOK, controllerwire.PipelineProfile(*prof))
	return nil
}

func runProfileObservations(ctx context.Context, t *store.Tenant, run *store.Run) (map[string]store.ProfileObservation, error) {
	observations := map[string]store.ProfileObservation{}
	if run == nil || run.Pipeline == "" {
		return observations, nil
	}
	nodes, err := t.ListNodes(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	var runPeakCores float64
	var runPeakMem int64
	measured := 0
	runValid := true
	runPartial := false
	for _, n := range nodes {
		if n.Outcome == "memoized" {
			continue
		}
		attempts := 0
		for _, attempt := range n.ExecutionAttempts {
			if attempt.RunID == run.ID {
				attempts++
			}
		}
		if n.Status != "done" || n.Outcome != "success" || n.AttemptsConsumed > 1 || attempts > 1 {
			runValid = false
			continue
		}
		samples, err := t.ListNodeMetrics(ctx, run.ID, n.NodeID)
		if err != nil {
			return nil, err
		}
		if len(samples) == 0 {
			runValid = false
			continue
		}
		peakCores, peakMem, hasCPU := samplePeaks(samples)
		if !hasCPU {
			runValid = false
			continue
		}
		partial := false
		for _, sample := range samples {
			partial = partial || sample.Kind == store.MetricPartial
		}
		runPartial = runPartial || partial
		measured++
		observations[n.NodeID] = store.ProfileObservation{
			Partial:         partial,
			Duration:        nodeMetricSpan(readings(samples)),
			PeakCores:       peakCores,
			PeakMemoryBytes: peakMem,
			CPUMeasured:     true,
		}
		runPeakCores = max(runPeakCores, peakCores)
		if peakMem > runPeakMem {
			runPeakMem = peakMem
		}
	}
	// safety: worker timestamps do not establish simultaneous resource usage.
	if measured != 1 || !runValid {
		return observations, nil
	}
	observations[""] = store.ProfileObservation{
		Partial:         runPartial,
		Duration:        runDuration(run),
		PeakCores:       runPeakCores,
		PeakMemoryBytes: runPeakMem,
		CPUMeasured:     true,
	}
	return observations, nil
}

func (s *Server) emitNodeDrift(ctx context.Context, t *store.Tenant, run *store.Run, nodeID string) {
	prof, err := t.GetPipelineProfile(ctx, run.Pipeline, nodeID)
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
	if err := t.AppendEvent(ctx, run.ID, nodeID, runstate.EventResourcePinDrift, payload); err != nil {
		s.logger.Warn("append run event", "kind", runstate.EventResourcePinDrift, "run_id", run.ID, "node_id", nodeID, "err", err)
	}
}

func samplePeaks(samples []store.MetricSample) (float64, int64, bool) {
	var hasCPU, unknown bool
	var cores float64
	var mem int64
	for _, s := range samples {
		unknown = unknown || s.Kind == store.MetricUnknown
		if s.Kind == store.MetricInterval {
			hasCPU = true
			cores = max(cores, float64(s.CPUMillicores)/1000.0)
		}
		if s.MemoryBytes > mem {
			mem = s.MemoryBytes
		}
	}
	return cores, mem, hasCPU && !unknown
}

func readings(samples []store.MetricSample) []store.MetricSample {
	out := make([]store.MetricSample, 0, len(samples))
	for _, sample := range samples {
		if !sample.Marker() {
			out = append(out, sample)
		}
	}
	return out
}

func nodeMetricSpan(samples []store.MetricSample) (d time.Duration) {
	if len(samples) < 2 {
		return 0
	}
	return samples[len(samples)-1].TS.Sub(samples[0].TS)
}

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

// safety: a wire caller cannot post a negative or an implausible usage figure.
const (
	maxProfileDuration = 365 * 24 * time.Hour
	maxProfileBytes    = 1 << 50
)

func boundedProfileValue(name string, value, limit float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%s must be a finite number", name)
	}
	if value < 0 {
		return fmt.Errorf("%s must be >= 0", name)
	}
	if value > limit {
		return fmt.Errorf("%s exceeds the %g ceiling", name, limit)
	}
	return nil
}
