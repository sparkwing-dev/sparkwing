package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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
	return "%" + r.Replace(asciiLower(term)) + "%"
}

// safety: text search folds ASCII letters only, on every backend: SQLite's LOWER and LIKE
// fold nothing else, so folding more in PostgreSQL or Go would select different runs.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

func asciiLowerSQL(dialect Dialect, col string) string {
	if dialect == DialectPostgres {
		return "TRANSLATE(" + col + ", 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz')"
	}
	return "LOWER(" + col + ")"
}

// safety: each clause filters the whole store, because a filter over one fetched page hides matches on the others.
func runDisplayFilterClauses(dialect Dialect, f RunFilter) []sqlClause {
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
			// safety: equality preserves repository-name case on both SQL dialects.
			suffix := "/" + name
			n := utf8.RuneCountInString(suffix)
			parts[i] = "(" + runRepoNameSQL + " = ? OR (LENGTH(" + runRepoNameSQL + ") >= ? AND SUBSTR(" + runRepoNameSQL +
				", LENGTH(" + runRepoNameSQL + ") - ? + 1) = ?))"
			args = append(args, name, n, n, suffix)
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
			parts[i] = asciiLowerSQL(dialect, col) + ` LIKE ? ESCAPE '\'`
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

// RunCursorKey is the started_at value a cursor names for a run that started at
// startedAt; a run that never started stores zero.
func RunCursorKey(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	return startedAt.UnixNano()
}
