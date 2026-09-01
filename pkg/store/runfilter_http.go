package store

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

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
