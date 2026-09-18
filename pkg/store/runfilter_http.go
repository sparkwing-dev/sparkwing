package store

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxRunListLimit is the largest page of runs a list query may ask for. A
// larger ask is clamped rather than rejected.
const MaxRunListLimit = 1000

const maxRunListFetch = MaxRunListLimit + 1

// RunFilterVersion is what a controller reports for the run-list filters it understands.
// A "1" controller ignores the cursor silently rather than refusing it, so a caller that
// needs the cursor checks this before trusting a page.
const RunFilterVersion = "2"

// SupportsRunIdentityFilters reports whether a controller announcing version
// serves the branch, SHA and repo filters natively.
func SupportsRunIdentityFilters(version string) bool { return runFilterVersion(version) >= 1 }

func SupportsRunCursor(version string) bool { return runFilterVersion(version) >= 2 }

func runFilterVersion(version string) int {
	n, err := strconv.Atoi(strings.TrimSpace(version))
	if err != nil {
		return 0
	}
	return n
}

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
	if v := q.Get("after_id"); v != "" {
		n, err := strconv.ParseInt(q.Get("after_started_at"), 10, 64)
		if err == nil {
			f.AfterStartedAt, f.AfterID = n, v
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			// safety: an unbounded limit materializes every run row, plan and args blobs included.
			f.Limit = min(n, maxRunListFetch)
		}
	}
	return f
}

// ParseRunFilterValidated rejects invalid public query values.
func ParseRunFilterValidated(q url.Values) (RunFilter, error) {
	f := ParseRunFilter(q)
	// safety: a cursor half-read would silently serve the first page again, which
	// reads to the caller as a result set that never advances.
	id, instant := q.Get("after_id"), q.Get("after_started_at")
	if (id != "" || instant != "") && !f.HasCursor() {
		return RunFilter{}, fmt.Errorf(
			"a cursor needs both after_id and a numeric after_started_at, got after_id=%q after_started_at=%q", id, instant)
	}
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
