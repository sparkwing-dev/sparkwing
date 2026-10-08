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
const RunFilterVersion = "3"

// SupportsRunIdentityFilters reports whether a controller announcing version
// serves the branch, SHA and repo filters natively.
func SupportsRunIdentityFilters(version string) bool { return runFilterVersion(version) >= 1 }

func SupportsRunCursor(version string) bool { return runFilterVersion(version) >= 2 }

// SupportsRunDisplayFilters reports whether a controller announcing version serves the
// exclusions, trigger, repository name, time range and text filters and the before cursor.
func SupportsRunDisplayFilters(version string) bool { return runFilterVersion(version) >= 3 }

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
		f.DeclaredRepos = splitCSV(v)
	}
	if v := q.Get("repo_url"); v != "" {
		f.RepoURLs = splitCSV(v)
	}
	for param, dst := range map[string]*[]string{
		"exclude_pipeline":       &f.ExcludePipelines,
		"exclude_status":         &f.ExcludeStatuses,
		"exclude_git_sha":        &f.ExcludeGitSHAPrefixes,
		"exclude_git_branch":     &f.ExcludeGitBranches,
		"trigger_source":         &f.TriggerSources,
		"exclude_trigger_source": &f.ExcludeTriggerSources,
		"repo_name":              &f.RepoNames,
		"exclude_repo_name":      &f.ExcludeRepoNames,
	} {
		if v := q.Get(param); v != "" {
			*dst = splitCSV(v)
		}
	}
	f.RootOnly, _ = strconv.ParseBool(q.Get("root_only"))
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.Since = time.Now().Add(-d)
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, q.Get("started_after")); err == nil && t.After(f.Since) {
		f.Since = t
	}
	for param, dst := range map[string]*time.Time{
		"started_before":  &f.StartedBefore,
		"finished_after":  &f.FinishedAfter,
		"finished_before": &f.FinishedBefore,
	} {
		if t, err := time.Parse(time.RFC3339Nano, q.Get(param)); err == nil {
			*dst = t
		}
	}
	f.Text, f.ExcludeText = ParseRunSearch(q.Get("q"))
	if v := q.Get("after_id"); v != "" {
		n, err := strconv.ParseInt(q.Get("after_started_at"), 10, 64)
		if err == nil {
			f.AfterStartedAt, f.AfterID = n, v
		}
	}
	if v := q.Get("before_id"); v != "" {
		n, err := strconv.ParseInt(q.Get("before_started_at"), 10, 64)
		if err == nil {
			f.BeforeStartedAt, f.BeforeID = n, v
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
	id, instant = q.Get("before_id"), q.Get("before_started_at")
	if (id != "" || instant != "") && !f.HasBeforeCursor() {
		return RunFilter{}, fmt.Errorf(
			"a cursor needs both before_id and a numeric before_started_at, got before_id=%q before_started_at=%q", id, instant)
	}
	if f.HasCursor() && f.HasBeforeCursor() {
		return RunFilter{}, errBothCursors
	}
	for _, param := range []string{"started_after", "started_before", "finished_after", "finished_before"} {
		if v := q.Get(param); v != "" {
			if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
				return RunFilter{}, fmt.Errorf("%s must be an RFC 3339 time, got %q", param, v)
			}
		}
	}
	if _, err := normalizeSHAPrefixes(f.GitSHAPrefixes); err != nil {
		return RunFilter{}, err
	}
	if _, err := normalizeSHAPrefixes(f.ExcludeGitSHAPrefixes); err != nil {
		return RunFilter{}, err
	}
	return f, nil
}

// ParseRunSearch splits a run search into the terms a run must contain and the terms it
// must not. Terms are separated by whitespace; one written -term, or after a lone -, is
// excluded.
func ParseRunSearch(s string) (include, exclude []string) {
	negate := false
	for _, term := range strings.Fields(s) {
		if term == "-" {
			negate = true
			continue
		}
		if len(term) > 1 && term[0] == '-' {
			term, negate = term[1:], true
		}
		if negate {
			exclude = append(exclude, term)
		} else {
			include = append(include, term)
		}
		negate = false
	}
	return include, exclude
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
