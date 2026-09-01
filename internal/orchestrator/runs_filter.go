package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type SearchTerms struct {
	Include []string
	Exclude []string
}

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

func ParseLooseDuration(v string) (time.Duration, error) {
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
		return scaleDuration(days, 24, v)
	case 'w':
		weeks, err := time.ParseDuration(v[:len(v)-1] + "h")
		if err != nil {
			return 0, err
		}
		return scaleDuration(weeks, 24*7, v)
	default:
		return time.ParseDuration(v)
	}
}

func scaleDuration(value time.Duration, factor int64, raw string) (time.Duration, error) {
	const (
		maxDuration = time.Duration(1<<63 - 1)
		minDuration = time.Duration(-1 << 63)
	)
	if value > maxDuration/time.Duration(factor) || value < minDuration/time.Duration(factor) {
		return 0, fmt.Errorf("duration %q overflows", raw)
	}
	return value * time.Duration(factor), nil
}

func SplitExcludes(values []string) (include, exclude []string) {
	for _, v := range values {
		if strings.HasPrefix(v, "!") {
			exclude = append(exclude, v[1:])
			continue
		}
		include = append(include, v)
	}
	return include, exclude
}

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
	if d, err := ParseLooseDuration(v); err == nil {
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
