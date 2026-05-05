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

// handleTrends aggregates the runs table into hourly buckets over the
// last N hours (default 24, capped at 14d). Cached runs = every node
// finished with outcome=cached.
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

	// Bucket size: hour for <=48h windows, 3 hours for <=7 days,
	// else daily.
	bucketDur := time.Hour
	switch {
	case hours > 48 && hours <= 7*24:
		bucketDur = 3 * time.Hour
	case hours > 7*24:
		bucketDur = 24 * time.Hour
	}
	bucketNs := int64(bucketDur)

	query := `
SELECT id, pipeline, status, started_at, finished_at
  FROM runs
 WHERE started_at >= ?
`
	args := []any{cutoff.UnixNano()}
	if pipeline != "" {
		query += " AND pipeline = ?"
		args = append(args, pipeline)
	}
	query += " ORDER BY started_at ASC"

	rows, err := s.store.DB().QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	type runRow struct {
		id, pipeline, status string
		startedNs            int64
		finishedNs           *int64
	}
	var runs []runRow
	for rows.Next() {
		var rr runRow
		var fin *int64
		if err := rows.Scan(&rr.id, &rr.pipeline, &rr.status, &rr.startedNs, &fin); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		rr.finishedNs = fin
		runs = append(runs, rr)
	}

	// "Cached" == every node's outcome is cached or satisfied. Done
	// in Go to keep the SQL simple.
	cachedIDs := map[string]bool{}
	if len(runs) > 0 {
		cq := `SELECT run_id, outcome FROM nodes WHERE run_id IN (` + placeholders(len(runs)) + `)`
		cargs := make([]any, 0, len(runs))
		for _, rr := range runs {
			cargs = append(cargs, rr.id)
		}
		nrows, err := s.store.DB().QueryContext(r.Context(), cq, cargs...)
		if err == nil {
			byRun := map[string][]string{}
			for nrows.Next() {
				var runID, outcome string
				if nrows.Scan(&runID, &outcome) == nil {
					byRun[runID] = append(byRun[runID], outcome)
				}
			}
			nrows.Close()
			for id, outcomes := range byRun {
				allCached := len(outcomes) > 0
				for _, o := range outcomes {
					if o != "cached" && o != "satisfied" {
						allCached = false
						break
					}
				}
				if allCached {
					cachedIDs[id] = true
				}
			}
		}
	}

	type bucket struct {
		total, passed, failed, cached int
		durationsMs                   []int64
	}
	buckets := map[int64]*bucket{}
	for _, rr := range runs {
		b := rr.startedNs - (rr.startedNs % bucketNs)
		bkt, ok := buckets[b]
		if !ok {
			bkt = &bucket{}
			buckets[b] = bkt
		}
		bkt.total++
		switch {
		case cachedIDs[rr.id]:
			bkt.cached++
		case rr.status == "success":
			bkt.passed++
		case rr.status == "failed":
			bkt.failed++
		}
		if rr.finishedNs != nil {
			durMs := (*rr.finishedNs - rr.startedNs) / 1_000_000
			bkt.durationsMs = append(bkt.durationsMs, durMs)
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
		out = append(out, TrendPoint{
			Bucket:   time.Unix(0, k).UTC().Format(time.RFC3339),
			Total:    b.total,
			Passed:   b.passed,
			Failed:   b.failed,
			Cached:   b.cached,
			AvgDurMs: avg,
			P95DurMs: p95,
		})
	}

	resp := map[string]any{"points": out}
	if pipeline != "" {
		resp["pipeline"] = pipeline
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, 2*n-1)
	for i := range n {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
	}
	return string(out)
}
