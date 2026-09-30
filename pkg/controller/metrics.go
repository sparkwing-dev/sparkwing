package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type metricSample struct {
	Kind          store.MetricKind `json:"kind"`
	TS            string           `json:"ts"`
	CPUMillicores int64            `json:"cpu_millicores"`
	MemoryBytes   int64            `json:"memory_bytes"`
	CPUTimeNanos  int64            `json:"cpu_time_nanos,omitempty"`
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
		parsed, err := time.Parse(time.RFC3339Nano, body.TS)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ts = parsed
	}
	if err := s.store.AddNodeMetricSample(r.Context(), runID, nodeID, store.MetricSample{
		Kind:          body.Kind,
		TS:            ts,
		CPUMillicores: body.CPUMillicores,
		MemoryBytes:   body.MemoryBytes,
		CPUTime:       time.Duration(body.CPUTimeNanos),
	}); err != nil {
		if status := runLimitStatus(err); status != 0 {
			writeError(w, status, err)
			return
		}
		s.writeInternalError(w, r, "add node metric", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const (
	defaultMetricPage = 1000
	maxMetricPage     = store.MaxNodeMetricSamples
)

type metricsPage struct {
	Points     []metricSample `json:"points"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func metricsPageRequest(r *http.Request) (after time.Time, limit int, err error) {
	limit = defaultMetricPage
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxMetricPage {
			return time.Time{}, 0, fmt.Errorf("limit is an integer from 1 to %d", maxMetricPage)
		}
		limit = n
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if after, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return time.Time{}, 0, errors.New("cursor is the next_cursor of the previous page")
		}
	}
	return after, limit, nil
}

// safety: callers pass up to limit+1 samples; the one past the page is what
// sets next_cursor.
func newMetricsPage(samples []store.MetricSample, limit int) metricsPage {
	page := metricsPage{Points: make([]metricSample, 0, min(len(samples), limit))}
	for i, s := range samples {
		if i == limit {
			page.NextCursor = page.Points[i-1].TS
			break
		}
		page.Points = append(page.Points, metricSample{
			Kind:          s.Kind,
			TS:            s.TS.UTC().Format(time.RFC3339Nano),
			CPUMillicores: s.CPUMillicores,
			MemoryBytes:   s.MemoryBytes,
			CPUTimeNanos:  s.CPUTime.Nanoseconds(),
		})
	}
	return page
}

func (s *Server) handleGetNodeMetrics(w http.ResponseWriter, r *http.Request) {
	after, limit, err := metricsPageRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	samples, err := s.store.ListNodeMetricsPage(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), after, limit+1)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, newMetricsPage(samples, limit))
}

func runLimitStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrAnnotationTooLarge), errors.Is(err, store.ErrRunAnnotationBytes):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, store.ErrRunAnnotationLimit), errors.Is(err, store.ErrNodeMetricLimit):
		return http.StatusTooManyRequests
	}
	return 0
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
		s.writeInternalError(w, r, "add node usage", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
