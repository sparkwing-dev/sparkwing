package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type metricSample struct {
	TS            string `json:"ts"`
	CPUMillicores int64  `json:"cpu_millicores"`
	MemoryBytes   int64  `json:"memory_bytes"`
	// safety: zero is a sampler tick; nonzero is a per-command measurement.
	CPUTimeNanos int64 `json:"cpu_time_nanos,omitempty"`
}

func (s *Server) handleAddNodeMetric(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body metricSample
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ts := time.Now()
	if body.TS != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, body.TS); err == nil {
			ts = parsed
		}
	}
	if err := s.store.AddNodeMetricSample(r.Context(), runID, nodeID, store.MetricSample{
		TS:            ts,
		CPUMillicores: body.CPUMillicores,
		MemoryBytes:   body.MemoryBytes,
		CPUTime:       time.Duration(body.CPUTimeNanos),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetNodeMetrics(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	samples, err := s.store.ListNodeMetrics(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	points := make([]metricSample, 0, len(samples))
	for _, s := range samples {
		points = append(points, metricSample{
			TS:            s.TS.UTC().Format(time.RFC3339Nano),
			CPUMillicores: s.CPUMillicores,
			MemoryBytes:   s.MemoryBytes,
			CPUTimeNanos:  s.CPUTime.Nanoseconds(),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"points": points})
}

type nodeUsageReq struct {
	CPUTimeNanos int64 `json:"cpu_time_nanos,omitempty"`
	MaxRSSBytes  int64 `json:"max_rss_bytes,omitempty"`
	WallNanos    int64 `json:"wall_nanos,omitempty"`
}

func (b nodeUsageReq) validate() error {
	for _, f := range []struct {
		name  string
		value float64
		limit float64
	}{
		{"cpu_time_nanos", float64(b.CPUTimeNanos), float64(maxProfileDuration)},
		{"max_rss_bytes", float64(b.MaxRSSBytes), maxProfileBytes},
		{"wall_nanos", float64(b.WallNanos), float64(maxProfileDuration)},
	} {
		if err := boundedProfileValue(f.name, f.value, f.limit); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleAddNodeUsage(w http.ResponseWriter, r *http.Request) {
	var body nodeUsageReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := body.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.AddNodeUsage(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), store.NodeUsage{
		CPUTime:     time.Duration(body.CPUTimeNanos),
		MaxRSSBytes: body.MaxRSSBytes,
		Wall:        time.Duration(body.WallNanos),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
