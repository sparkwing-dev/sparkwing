// Client-side filters for `sparkwing runs list`. The store's SQL
// only narrows on pipeline / status / since today; everything else
// (branch / sha / error / free-form search / explicit excludes /
// finished-window) runs as a post-fetch pass here so the CLI can
// ship parity with the dashboard filter bar without a controller
// schema change.
package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// SearchTerms is the parsed shape of `--search "deploy -canary"`.
// `Include` matches AND (each term must appear); `Exclude` is
// reject-on-match.
type SearchTerms struct {
	Include []string
	Exclude []string
}

// ParseSearch splits the raw search query on whitespace; a leading
// `-` on a term marks it as exclude. Empty query returns a zero
// SearchTerms.
func ParseSearch(raw string) SearchTerms {
	var out SearchTerms
	for _, tok := range strings.Fields(raw) {
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			out.Exclude = append(out.Exclude, strings.ToLower(tok[1:]))
			continue
		}
		out.Include = append(out.Include, strings.ToLower(tok))
	}
	return out
}

// parseLooseDuration accepts time.ParseDuration plus the `d` and
// `w` suffixes the dashboard filter bar uses ("7d", "2w"). A bare
// number is rejected -- callers must supply a unit so "7" doesn't
// silently mean "7 nanoseconds".
func parseLooseDuration(v string) (time.Duration, error) {
	if v == "" {
		return 0, errors.New("empty duration")
	}
	last := v[len(v)-1]
	switch last {
	case 'd':
		days, err := time.ParseDuration(v[:len(v)-1] + "h")
		if err != nil {
			return 0, err
		}
		return days * 24, nil
	case 'w':
		weeks, err := time.ParseDuration(v[:len(v)-1] + "h")
		if err != nil {
			return 0, err
		}
		return weeks * 24 * 7, nil
	default:
		return time.ParseDuration(v)
	}
}

// SplitExcludes splits a repeatable filter slice into the include
// and exclude lists based on the `!` prefix convention.
func SplitExcludes(values []string) (include, exclude []string) {
	for _, v := range values {
		if strings.HasPrefix(v, "!") {
			exclude = append(exclude, v[1:])
			continue
		}
		include = append(include, v)
	}
	return
}

// ParseLooseDate accepts the same shapes the dashboard filter bar
// does:
//
//   - "today", "yesterday" (start-of-day local)
//   - "24h", "7d", "5m", "1h", etc. (Go duration → now-d)
//   - ISO date "2026-05-10" → start of that day local
//   - RFC3339 timestamp → exact
//
// Returns the absolute time on success.
func ParseLooseDate(raw string) (time.Time, error) {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return time.Time{}, errors.New("empty date")
	}
	now := time.Now()
	switch v {
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	case "yesterday":
		y := now.AddDate(0, 0, -1)
		return time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, y.Location()), nil
	}
	if d, err := parseLooseDuration(v); err == nil {
		return now.Add(-d), nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date %q (try today, yesterday, 24h, 7d, or 2026-05-10)", raw)
}

// CompiledFilter is the cooked filter set the post-fetch pass uses.
// Built once from ListOpts; applied with Matches per run.
type CompiledFilter struct {
	Branches       []string
	BranchExcludes []string
	SHAPrefixes    []string
	SHAExcludes    []string
	ErrorSubstr    string
	StatusExcludes []string
	PipelineExcl   []string
	Search         SearchTerms
	StartedAfter   time.Time
	StartedBefore  time.Time
	FinishedAfter  time.Time
	FinishedBefore time.Time
}

// HasAny reports whether any client-side filter is active. When
// false the caller can skip the matching pass entirely.
func (f CompiledFilter) HasAny() bool {
	if len(f.Branches)+len(f.BranchExcludes)+len(f.SHAPrefixes)+len(f.SHAExcludes) > 0 {
		return true
	}
	if f.ErrorSubstr != "" {
		return true
	}
	if len(f.StatusExcludes)+len(f.PipelineExcl) > 0 {
		return true
	}
	if len(f.Search.Include)+len(f.Search.Exclude) > 0 {
		return true
	}
	if !f.StartedAfter.IsZero() || !f.StartedBefore.IsZero() {
		return true
	}
	if !f.FinishedAfter.IsZero() || !f.FinishedBefore.IsZero() {
		return true
	}
	return false
}

// Matches returns true when r passes every active filter.
func (f CompiledFilter) Matches(r *store.Run) bool {
	if len(f.Branches) > 0 && !containsString(f.Branches, r.GitBranch) {
		return false
	}
	if containsString(f.BranchExcludes, r.GitBranch) {
		return false
	}
	if len(f.SHAPrefixes) > 0 && !hasAnyPrefix(r.GitSHA, f.SHAPrefixes) {
		return false
	}
	if hasAnyPrefix(r.GitSHA, f.SHAExcludes) {
		return false
	}
	if f.ErrorSubstr != "" && !strings.Contains(strings.ToLower(r.Error), strings.ToLower(f.ErrorSubstr)) {
		return false
	}
	if containsString(f.StatusExcludes, r.Status) {
		return false
	}
	if containsString(f.PipelineExcl, r.Pipeline) {
		return false
	}
	if !f.StartedAfter.IsZero() && r.StartedAt.Before(f.StartedAfter) {
		return false
	}
	if !f.StartedBefore.IsZero() && r.StartedAt.After(f.StartedBefore) {
		return false
	}
	if !f.FinishedAfter.IsZero() {
		if r.FinishedAt == nil || r.FinishedAt.Before(f.FinishedAfter) {
			return false
		}
	}
	if !f.FinishedBefore.IsZero() {
		if r.FinishedAt == nil || r.FinishedAt.After(f.FinishedBefore) {
			return false
		}
	}
	if len(f.Search.Include) > 0 || len(f.Search.Exclude) > 0 {
		hay := strings.ToLower(strings.Join([]string{
			r.ID, r.Pipeline, r.GitBranch, r.GitSHA, r.Error,
		}, " "))
		for _, t := range f.Search.Include {
			if !strings.Contains(hay, t) {
				return false
			}
		}
		for _, t := range f.Search.Exclude {
			if strings.Contains(hay, t) {
				return false
			}
		}
	}
	return true
}

func containsString(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// applyClientFilters narrows runs in place per the CompiledFilter.
// Order-preserving so renderRunList still gets newest-first input.
func applyClientFilters(runs []*store.Run, f CompiledFilter) []*store.Run {
	if !f.HasAny() {
		return runs
	}
	out := runs[:0:0]
	for _, r := range runs {
		if f.Matches(r) {
			out = append(out, r)
		}
	}
	return out
}
