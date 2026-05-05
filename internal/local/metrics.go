package local

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// metricSample is the JSON wire type. Field names match the
// dashboard's MetricPoint so POST + GET share shapes.
type metricSample struct {
	TS            string `json:"ts"` // RFC3339
	CPUMillicores int64  `json:"cpu_millicores"`
	MemoryBytes   int64  `json:"memory_bytes"`
}

// handleAddNodeMetric appends one resource sample for a node. Single
// sample per request; sample rate is low (~0.5 Hz).
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
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetNodeMetrics returns the sample series for one node.
// Response shape matches the dashboard's NodeMetrics.
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
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"points": points})
}
