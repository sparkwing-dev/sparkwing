package store

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxRunListLimit is the largest number of runs a list query may ask
// for. Higher values are clamped rather than rejected.
const MaxRunListLimit = 1000

// ParseRunFilter accepts the public run-list query parameters. Unknown
// parameters are ignored.
func ParseRunFilter(q url.Values) RunFilter {
	var f RunFilter
	if v := q.Get("pipeline"); v != "" {
		f.Pipelines = splitCSV(v)
	}
	if v := q.Get("status"); v != "" {
		f.Statuses = splitCSV(v)
	}
	if v := q.Get("git_sha"); v != "" {
		f.GitSHAPrefixes = splitCSV(v)
	}
	if v := q.Get("git_branch"); v != "" {
		f.GitBranches = splitCSV(v)
	}
	if v := q.Get("repo"); v != "" {
		f.Repos = splitCSV(v)
	}
	if v := q.Get("repo_url"); v != "" {
		f.RepoURLs = splitCSV(v)
	}
	f.RootOnly, _ = strconv.ParseBool(q.Get("root_only"))
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.Since = time.Now().Add(-d)
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			// safety: an unbounded limit materializes every run row, plan and args blobs included.
			f.Limit = min(n, MaxRunListLimit)
		}
	}
	return f
}

// ParseRunFilterValidated rejects invalid public query values.
func ParseRunFilterValidated(q url.Values) (RunFilter, error) {
	f := ParseRunFilter(q)
	for _, prefix := range f.GitSHAPrefixes {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix == "" || strings.IndexFunc(prefix, func(r rune) bool {
			return (r < '0' || r > '9') && (r < 'a' || r > 'f')
		}) >= 0 {
			return RunFilter{}, fmt.Errorf("git SHA prefix %q must contain hexadecimal characters", prefix)
		}
	}
	return f, nil
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
