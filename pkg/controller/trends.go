package controller

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// TrendPoint matches web/src/lib/api.ts:TrendPoint. One bucket of
// runs grouped by hour (or larger when the window is wide).
type TrendPoint struct {
	Bucket    string `json:"bucket"` // RFC3339 at the bucket boundary
	Total     int    `json:"total"`
	Passed    int    `json:"passed"`
	Failed    int    `json:"failed"`
	Cached    int    `json:"cached"`
	AvgDurMs  int64  `json:"avg_dur_ms"`
	P95DurMs  int64  `json:"p95_dur_ms"`
	AvgWaitMs int64  `json:"avg_wait_ms"`
}

func (s *Server) handleTrends(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}
	if hours > 14*24 {
		hours = 14 * 24
	}
	pipeline := r.URL.Query().Get("pipeline")

	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)

	bucketDur := time.Hour
	switch {
	case hours > 48 && hours <= 7*24:
		bucketDur = 3 * time.Hour
	case hours > 7*24:
		bucketDur = 24 * time.Hour
	}
	bucketNs := int64(bucketDur)

	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runs, err := tenant.ListRunTrends(r.Context(), cutoff, pipeline)
	if err != nil {
		s.writeInternalError(w, r, "list run trends", err)
		return
	}

	type bucket struct {
		total, passed, failed, cached int
		durationsMs                   []int64
		waitSumNs                     int64
		waitCount                     int
	}
	buckets := map[int64]*bucket{}
	for _, rr := range runs {
		startedNs := rr.StartedAt.UnixNano()
		b := startedNs - (startedNs % bucketNs)
		bkt, ok := buckets[b]
		if !ok {
			bkt = &bucket{}
			buckets[b] = bkt
		}
		bkt.total++
		switch {
		case rr.Cached:
			bkt.cached++
		case rr.Status == "success":
			bkt.passed++
		case rr.Status == "failed":
			bkt.failed++
		}
		if rr.FinishedAt != nil {
			durMs := rr.FinishedAt.Sub(rr.StartedAt).Milliseconds()
			bkt.durationsMs = append(bkt.durationsMs, durMs)
		}
		createdNs := rr.CreatedAt.UnixNano()
		if createdNs > 0 && createdNs <= startedNs {
			bkt.waitSumNs += startedNs - createdNs
			bkt.waitCount++
		}
	}

	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	out := make([]TrendPoint, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		sort.Slice(b.durationsMs, func(i, j int) bool { return b.durationsMs[i] < b.durationsMs[j] })
		var avg, p95 int64
		if len(b.durationsMs) > 0 {
			sum := int64(0)
			for _, d := range b.durationsMs {
				sum += d
			}
			avg = sum / int64(len(b.durationsMs))
			p95Idx := int(math.Ceil(float64(len(b.durationsMs))*0.95)) - 1
			if p95Idx < 0 {
				p95Idx = 0
			}
			p95 = b.durationsMs[p95Idx]
		}
		var avgWait int64
		if b.waitCount > 0 {
			avgWait = b.waitSumNs / int64(b.waitCount) / int64(time.Millisecond)
		}
		out = append(out, TrendPoint{
			Bucket:    time.Unix(0, k).UTC().Format(time.RFC3339),
			Total:     b.total,
			Passed:    b.passed,
			Failed:    b.failed,
			Cached:    b.cached,
			AvgDurMs:  avg,
			P95DurMs:  p95,
			AvgWaitMs: avgWait,
		})
	}

	resp := map[string]any{"points": out}
	if pipeline != "" {
		resp["pipeline"] = pipeline
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
