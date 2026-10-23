package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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

type setPinReq struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
}

// safety: a pin becomes a hard Kubernetes limit for every later run of the
// pipeline, so a negative component is a bad request rather than a clear.
func (b setPinReq) validate() error {
	if err := boundedProfileValue("cores", b.Cores, maxProfileCores); err != nil {
		return err
	}
	return boundedProfileValue("memory_bytes", float64(b.MemoryBytes), maxProfileBytes)
}

func (s *Server) handleSetPipelinePin(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body setPinReq
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if err := body.validate(); err != nil {
		return invalidEngineBody(err.Error())
	}
	tenant, err := s.sizingStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	err = tenant.SetProfilePin(r.Context(), a, body.Cores, body.MemoryBytes)
	if err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// safety: omit sustained cores because these profiles become hard Kubernetes
// CPU limits; charging a local-host plateau would throttle spiky pods.
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
	if err := t.AppendEvent(ctx, run.ID, nodeID, "resource_pin_drift", payload); err != nil {
		s.logger.Warn("append run event", "kind", "resource_pin_drift", "run_id", run.ID, "node_id", nodeID, "err", err)
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

type profileObservationReq struct {
	Partial          bool    `json:"partial,omitempty"`
	DurationNanos    int64   `json:"duration_nanos,omitempty"`
	PeakCores        float64 `json:"peak_cores,omitempty"`
	PeakMemoryBytes  int64   `json:"peak_memory_bytes,omitempty"`
	SustainedCores   float64 `json:"sustained_cores,omitempty"`
	CPUMeasured      bool    `json:"cpu_measured,omitempty"`
	PlanHash         string  `json:"plan_hash,omitempty"`
	Contended        bool    `json:"contended,omitempty"`
	FloorCores       float64 `json:"floor_cores,omitempty"`
	FloorMemoryBytes int64   `json:"floor_memory_bytes,omitempty"`
}

// safety: these figures become the price of every later run of the pipeline,
// so a wire caller cannot post a negative or an implausible one.
const (
	maxProfileDuration = 365 * 24 * time.Hour
	maxProfileCores    = 1 << 20
	maxProfileBytes    = 1 << 50
)

func (b profileObservationReq) validate() error {
	for _, f := range []struct {
		name  string
		value float64
		limit float64
	}{
		{"duration_nanos", float64(b.DurationNanos), float64(maxProfileDuration)},
		{"peak_cores", b.PeakCores, maxProfileCores},
		{"peak_memory_bytes", float64(b.PeakMemoryBytes), maxProfileBytes},
		{"sustained_cores", b.SustainedCores, maxProfileCores},
		{"floor_cores", b.FloorCores, maxProfileCores},
		{"floor_memory_bytes", float64(b.FloorMemoryBytes), maxProfileBytes},
	} {
		if err := boundedProfileValue(f.name, f.value, f.limit); err != nil {
			return err
		}
	}
	return nil
}

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

func (b profileObservationReq) observation() store.ProfileObservation {
	return store.ProfileObservation{
		Partial:          b.Partial,
		Duration:         time.Duration(b.DurationNanos),
		PeakCores:        b.PeakCores,
		PeakMemoryBytes:  b.PeakMemoryBytes,
		SustainedCores:   b.SustainedCores,
		CPUMeasured:      b.CPUMeasured,
		PlanHash:         b.PlanHash,
		Contended:        b.Contended,
		FloorCores:       b.FloorCores,
		FloorMemoryBytes: b.FloorMemoryBytes,
	}
}

func (s *Server) handleRecordProfileObservation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body profileObservationReq
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if err := body.validate(); err != nil {
		return invalidEngineBody(err.Error())
	}
	tenant, err := s.sizingStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := tenant.RecordProfileObservation(r.Context(), a, body.observation()); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleRecordContention(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.sizingStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := tenant.RecordContention(r.Context(), a); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type waitObservationReq struct {
	WaitNanos int64 `json:"wait_nanos"`
}

func (b waitObservationReq) validate() error {
	return boundedProfileValue("wait_nanos", float64(b.WaitNanos), float64(maxProfileDuration))
}

func (s *Server) handleRecordWaitObservation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body waitObservationReq
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if err := body.validate(); err != nil {
		return invalidEngineBody(err.Error())
	}
	tenant, err := s.sizingStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := tenant.RecordWaitObservation(r.Context(), a, time.Duration(body.WaitNanos)); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
