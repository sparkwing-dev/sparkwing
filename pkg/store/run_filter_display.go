package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type sqlClause struct {
	sql  string
	args []any
}

const runRepoNameSQL = `(CASE WHEN declared_repo <> '' THEN declared_repo WHEN github_repo <> '' THEN github_repo ELSE 'unknown' END)`

var runTextColumns = []string{"id", "pipeline", "declared_repo", "github_repo", "git_branch", "git_sha", "error", "trigger_source", "status"}

func normalizeSHAPrefixes(prefixes []string) ([]string, error) {
	if len(prefixes) == 0 {
		return prefixes, nil
	}
	out := make([]string, len(prefixes))
	for i, prefix := range prefixes {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix == "" || strings.IndexFunc(prefix, func(r rune) bool {
			return (r < '0' || r > '9') && (r < 'a' || r > 'f')
		}) >= 0 {
			return nil, fmt.Errorf("git SHA prefix %q must contain hexadecimal characters", prefix)
		}
		out[i] = prefix
	}
	return out, nil
}

func shaPrefixClause(prefixes []string) (string, []any) {
	parts := make([]string, 0, len(prefixes))
	values := make([]any, 0, len(prefixes)*2)
	for _, prefix := range prefixes {
		if upper, ok := prefixUpperBound(prefix); ok {
			parts = append(parts, "(git_sha >= ? AND git_sha < ?)")
			values = append(values, prefix, upper)
		} else {
			parts = append(parts, "git_sha = ?")
			values = append(values, prefix)
		}
	}
	return "(" + strings.Join(parts, " OR ") + ")", values
}

func likeContains(term string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.ToLower(term)) + "%"
}

func likeSuffix(suffix string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(suffix)
}

// safety: each clause filters the whole store, because a filter over one fetched page hides matches on the others.
func runDisplayFilterClauses(f RunFilter) []sqlClause {
	var out []sqlClause
	for _, list := range []struct {
		clause string
		values []string
	}{
		{"trigger_source IN", f.TriggerSources},
		{"pipeline NOT IN", f.ExcludePipelines},
		{"status NOT IN", f.ExcludeStatuses},
		{"git_branch NOT IN", f.ExcludeGitBranches},
		{"trigger_source NOT IN", f.ExcludeTriggerSources},
	} {
		if len(list.values) > 0 {
			out = append(out, sqlClause{list.clause + " (" + placeholders(len(list.values)) + ")", stringsToAny(list.values)})
		}
	}
	if len(f.ExcludeGitSHAPrefixes) > 0 {
		clause, args := shaPrefixClause(f.ExcludeGitSHAPrefixes)
		out = append(out, sqlClause{"NOT " + clause, args})
	}
	repoNames := func(negate string, names []string) {
		if len(names) == 0 {
			return
		}
		parts := make([]string, len(names))
		var args []any
		for i, name := range names {
			parts[i] = "(" + runRepoNameSQL + " = ? OR " + runRepoNameSQL + ` LIKE ? ESCAPE '\')`
			args = append(args, name, likeSuffix("/"+name))
		}
		out = append(out, sqlClause{negate + "(" + strings.Join(parts, " OR ") + ")", args})
	}
	repoNames("", f.RepoNames)
	repoNames("NOT ", f.ExcludeRepoNames)
	if !f.StartedBefore.IsZero() {
		out = append(out, sqlClause{"started_at <= ?", []any{f.StartedBefore.UnixNano()}})
	}
	if !f.FinishedAfter.IsZero() {
		out = append(out, sqlClause{"(finished_at IS NOT NULL AND finished_at >= ?)", []any{f.FinishedAfter.UnixNano()}})
	}
	if !f.FinishedBefore.IsZero() {
		out = append(out, sqlClause{"(finished_at IS NOT NULL AND finished_at <= ?)", []any{f.FinishedBefore.UnixNano()}})
	}
	text := func(negate, term string) {
		parts := make([]string, len(runTextColumns))
		args := make([]any, len(runTextColumns))
		for i, col := range runTextColumns {
			parts[i] = "LOWER(" + col + `) LIKE ? ESCAPE '\'`
			args[i] = likeContains(term)
		}
		out = append(out, sqlClause{negate + "(" + strings.Join(parts, " OR ") + ")", args})
	}
	for _, term := range f.Text {
		text("", term)
	}
	for _, term := range f.ExcludeText {
		text("NOT ", term)
	}
	return out
}

var errBothCursors = errors.New("a run listing pages after one run or before one, not both")

func runBeforeCursorClause(beforeStartedAt int64) string {
	if beforeStartedAt <= 0 {
		return "(started_at > 0 OR (started_at <= 0 AND id > ?))"
	}
	return "(started_at > ? OR (started_at = ? AND id > ?))"
}

func runBeforeCursorArgs(f RunFilter) []any {
	if f.BeforeStartedAt <= 0 {
		return []any{f.BeforeID}
	}
	return []any{f.BeforeStartedAt, f.BeforeStartedAt, f.BeforeID}
}

// safety: mirrors runRepoNameSQL, so the in-memory filter selects the runs the store does.
func runRepoName(r *Run) string {
	switch {
	case r.DeclaredRepo != "":
		return r.DeclaredRepo
	case r.GithubRepo != "":
		return r.GithubRepo
	}
	return "unknown"
}

// RunCursorKey is the started_at value a cursor names for a run that started at
// startedAt; a run that never started stores zero.
func RunCursorKey(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	return startedAt.UnixNano()
}

// FilterRuns applies f to runs a backend fetched whole, with the order, cursors and
// limit [Store.ListRuns] gives the same filter.
func FilterRuns(runs []*Run, f RunFilter) ([]*Run, error) {
	var err error
	if f.GitSHAPrefixes, err = normalizeSHAPrefixes(f.GitSHAPrefixes); err != nil {
		return nil, err
	}
	if f.ExcludeGitSHAPrefixes, err = normalizeSHAPrefixes(f.ExcludeGitSHAPrefixes); err != nil {
		return nil, err
	}
	if f.HasCursor() && f.HasBeforeCursor() {
		return nil, errBothCursors
	}
	out := make([]*Run, 0, len(runs))
	for _, r := range runs {
		if r != nil && runMatches(r, f) {
			out = append(out, r)
		}
	}
	newerFirst := func(a, b *Run) int {
		if ka, kb := RunCursorKey(a.StartedAt), RunCursorKey(b.StartedAt); ka != kb {
			if ka > kb {
				return -1
			}
			return 1
		}
		return -strings.Compare(a.ID, b.ID)
	}
	slices.SortFunc(out, newerFirst)
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, maxRunListFetch)
	if f.HasBeforeCursor() {
		cursor := &Run{ID: f.BeforeID, StartedAt: time.Unix(0, f.BeforeStartedAt)}
		if f.BeforeStartedAt <= 0 {
			cursor.StartedAt = time.Time{}
		}
		newer := out[:0]
		for _, r := range out {
			if newerFirst(r, cursor) < 0 {
				newer = append(newer, r)
			}
		}
		return newer[max(len(newer)-limit, 0):], nil
	}
	if f.HasCursor() {
		cursor := &Run{ID: f.AfterID, StartedAt: time.Unix(0, f.AfterStartedAt)}
		if f.AfterStartedAt <= 0 {
			cursor.StartedAt = time.Time{}
		}
		older := out[:0]
		for _, r := range out {
			if newerFirst(r, cursor) > 0 {
				older = append(older, r)
			}
		}
		out = older
	}
	return out[:min(len(out), limit)], nil
}

func runMatches(r *Run, f RunFilter) bool {
	in := func(values []string, v string) bool { return slices.Contains(values, v) }
	shaMatch := func(prefixes []string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(r.GitSHA, p) {
				return true
			}
		}
		return false
	}
	repoMatch := func(names []string) bool {
		repo := runRepoName(r)
		for _, n := range names {
			if repo == n || strings.HasSuffix(repo, "/"+n) {
				return true
			}
		}
		return false
	}
	text := strings.ToLower(strings.Join([]string{r.ID, r.Pipeline, r.DeclaredRepo, r.GithubRepo, r.GitBranch, r.GitSHA, r.Error, r.TriggerSource, r.Status}, "\x00"))
	started := RunCursorKey(r.StartedAt)
	switch {
	case len(f.Pipelines) > 0 && !in(f.Pipelines, r.Pipeline),
		len(f.Statuses) > 0 && !in(f.Statuses, r.Status),
		len(f.GitBranches) > 0 && !in(f.GitBranches, r.GitBranch),
		len(f.DeclaredRepos) > 0 && !in(f.DeclaredRepos, r.DeclaredRepo),
		len(f.RepoURLs) > 0 && !in(f.RepoURLs, r.RepoURL),
		len(f.TriggerSources) > 0 && !in(f.TriggerSources, r.TriggerSource),
		len(f.GitSHAPrefixes) > 0 && !shaMatch(f.GitSHAPrefixes),
		len(f.RepoNames) > 0 && !repoMatch(f.RepoNames),
		in(f.ExcludePipelines, r.Pipeline),
		in(f.ExcludeStatuses, r.Status),
		in(f.ExcludeGitBranches, r.GitBranch),
		in(f.ExcludeTriggerSources, r.TriggerSource),
		len(f.ExcludeGitSHAPrefixes) > 0 && shaMatch(f.ExcludeGitSHAPrefixes),
		len(f.ExcludeRepoNames) > 0 && repoMatch(f.ExcludeRepoNames),
		f.ParentRunID != "" && r.ParentRunID != f.ParentRunID,
		f.ParentRunID == "" && f.RootOnly && r.ParentRunID != "",
		!f.Since.IsZero() && started < f.Since.UnixNano(),
		!f.StartedBefore.IsZero() && started > f.StartedBefore.UnixNano(),
		!f.FinishedAfter.IsZero() && (r.FinishedAt == nil || r.FinishedAt.Before(f.FinishedAfter)),
		!f.FinishedBefore.IsZero() && (r.FinishedAt == nil || r.FinishedAt.After(f.FinishedBefore)):
		return false
	}
	for _, term := range f.Text {
		if !strings.Contains(text, strings.ToLower(term)) {
			return false
		}
	}
	for _, term := range f.ExcludeText {
		if strings.Contains(text, strings.ToLower(term)) {
			return false
		}
	}
	return true
}
