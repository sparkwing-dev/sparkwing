package store

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ParseRunFilter accepts pipeline, status, since (Go duration),
// limit; unknown params ignored.
func ParseRunFilter(q url.Values) RunFilter {
	var f RunFilter
	if v := q.Get("pipeline"); v != "" {
		f.Pipelines = splitCSV(v)
	}
	if v := q.Get("status"); v != "" {
		f.Statuses = splitCSV(v)
	}
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.Since = time.Now().Add(-d)
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}
	return f
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
