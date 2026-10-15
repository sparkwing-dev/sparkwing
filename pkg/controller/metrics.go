package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) metricEndpoints() []Endpoint {
	engine := s.engineController
	if engine == nil {
		engine = &EngineController{Store: s.store}
	}
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/metrics", Authentication: capability.AuthCapability, Decode: engine.runTarget, Handle: s.handleAddJobMetric},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/usage", Authentication: capability.AuthCapability, Decode: engine.runTarget, Handle: s.handleAddJobUsage},
	}
}

type metricSample struct {
	Kind          store.MetricKind `json:"kind"`
	TS            string           `json:"ts"`
	CPUMillicores int64            `json:"cpu_millicores"`
	MemoryBytes   int64            `json:"memory_bytes"`
	CPUTimeNanos  int64            `json:"cpu_time_nanos,omitempty"`
}

func (s *Server) handleAddJobMetric(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body metricSample
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	ts := time.Now()
	if body.TS != "" {
		parsed, err := time.Parse(time.RFC3339Nano, body.TS)
		if err != nil {
			return invalidEngineBody("ts must be an RFC3339 timestamp")
		}
		ts = parsed
	}
	sample := store.MetricSample{Kind: body.Kind, TS: ts, CPUMillicores: body.CPUMillicores, MemoryBytes: body.MemoryBytes, CPUTime: time.Duration(body.CPUTimeNanos)}
	if err := sample.ValidateResourceValues(); err != nil {
		return invalidEngineBody(err.Error())
	}
	if !time.Unix(0, ts.UnixNano()).Equal(ts) {
		return invalidEngineBody("ts exceeds the nanosecond storage range")
	}
	writer, err := s.metricWriter(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	err = writer.WriteMetric(r.Context(), a, sample)
	if err != nil {
		return metricError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
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

func (s *Server) handleGetJobMetrics(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	after, limit, err := metricsPageRequest(r)
	if err != nil {
		return invalidEngineBody(err.Error())
	}
	samples, err := tenant.ListNodeMetricsPage(r.Context(), a.Target.RunID, a.Target.NodeID, after, limit+1)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, newMetricsPage(samples, limit))
	return nil
}

func metricError(err error) error {
	if errors.Is(err, store.ErrNodeMetricLimit) {
		return &apierr.Error{Code: apierr.RateLimited, Message: "job metric sample limit reached"}
	}
	return engineLookupError(err)
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

func (s *Server) handleAddJobUsage(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body nodeUsageReq
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if err := body.validate(); err != nil {
		return invalidEngineBody(err.Error())
	}
	writer, err := s.metricWriter(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := writer.WriteUsage(r.Context(), a, store.NodeUsage{CPUTime: time.Duration(body.CPUTimeNanos), MaxRSSBytes: body.MaxRSSBytes, Wall: time.Duration(body.WallNanos)}); err != nil {
		return metricError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
