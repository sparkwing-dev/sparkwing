package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

func (s *Server) handleSetPipelinePin(w http.ResponseWriter, r *http.Request) {
	pipeline := r.PathValue("name")
	nodeID := r.URL.Query().Get("node")
	var body setPinReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := body.validate(); err != nil {
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

// safety: omit sustained cores because these profiles become hard Kubernetes
// CPU limits; charging a local-host plateau would throttle spiky pods.
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

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

type profileObservationReq struct {
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

func (s *Server) handleRecordProfileObservation(w http.ResponseWriter, r *http.Request) {
	var body profileObservationReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := body.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.RecordProfileObservation(r.Context(), r.PathValue("name"),
		r.URL.Query().Get("node"), body.observation()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRecordContention(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RecordContention(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type waitObservationReq struct {
	WaitNanos int64 `json:"wait_nanos"`
}

func (b waitObservationReq) validate() error {
	return boundedProfileValue("wait_nanos", float64(b.WaitNanos), float64(maxProfileDuration))
}

func (s *Server) handleRecordWaitObservation(w http.ResponseWriter, r *http.Request) {
	var body waitObservationReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := body.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.RecordWaitObservation(r.Context(), r.PathValue("name"),
		time.Duration(body.WaitNanos)); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
