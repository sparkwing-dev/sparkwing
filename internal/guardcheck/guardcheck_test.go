package main

import (
	"slices"
	"strings"
	"testing"
)

func TestClassifyKeysDepguardByPackageEdgeAndForbidigoByCall(t *testing.T) {
	issue := func(linter, file, text string) lintIssue {
		i := lintIssue{FromLinter: linter, Text: text}
		i.Pos.Filename = file
		return i
	}
	got, err := classify([]lintIssue{
		issue("depguard", "internal/store/a.go", "import 'github.com/x/internal/source' is not allowed from list 'store-allowed-internal': why"),
		issue("depguard", "internal/store/b.go", "import 'github.com/x/internal/source' is not allowed from list 'store-allowed-internal': why"),
		issue("forbidigo", "internal/controller/c.go", "use of `s.store.ForTeam` forbidden because \"F1: widening\""),
		issue("forbidigo", "internal/controller/c.go", "use of `s.store.ForTeam` forbidden because \"F1: widening\""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"internal/store\tgithub.com/x/internal/source"}; !slices.Equal(got["depguard-store-allowed-internal.allow"], want) {
		t.Errorf("depguard entries = %q, want %q", got["depguard-store-allowed-internal.allow"], want)
	}
	if want := []string{"internal/controller/c.go\tForTeam", "internal/controller/c.go\tForTeam"}; !slices.Equal(got["forbidigo-f1.allow"], want) {
		t.Errorf("forbidigo entries = %q, want %q", got["forbidigo-f1.allow"], want)
	}
}

func TestClassifyRefusesAFindingItCannotKey(t *testing.T) {
	if _, err := classify([]lintIssue{{FromLinter: "errcheck", Text: "x"}}); err == nil {
		t.Fatal("an unexpected linter's finding must fail the run rather than pass unjudged")
	}
}

func TestCompareEntriesFailsBothANewFindingAndAStaleLine(t *testing.T) {
	got := compareEntries("forbidigo-f1.allow",
		[]string{"a.go\tForTeam", "a.go\tForTeam", "b.go\tAsOperator"},
		[]string{"a.go\tForTeam", "c.go\tForTeam"})
	if len(got) != 3 {
		t.Fatalf("problems = %q, want a second a.go call, b.go and the stale c.go", got)
	}
	if !strings.HasPrefix(got[0], "NEW") || !strings.HasPrefix(got[2], "GONE") || !strings.Contains(got[2], "c.go") {
		t.Errorf("problems = %q", got)
	}
}

func TestCompareCountsAsksForTheCountToFallWithTheCode(t *testing.T) {
	got := compareCounts(map[string]int{"internal/a": 3, "internal/b": 1},
		map[string][]string{"internal/a": {"X", "Y", "Z"}},
		[]string{"internal/a\t2", "internal/b\t4", "internal/c\t1"})
	want := []string{"NEW  unexport.allow: internal/a", "GONE unexport.allow: internal/b has 1", "GONE unexport.allow: internal/c has no"}
	if len(got) != len(want) {
		t.Fatalf("problems = %q", got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("problem %d = %q, want prefix %q", i, got[i], want[i])
		}
	}
}

func TestClassifyRefusesAFindingFromAnotherWorktree(t *testing.T) {
	for _, file := range []string{"../../1086/sparkwing/internal/store/a.go", "/elsewhere/internal/store/a.go"} {
		i := lintIssue{FromLinter: "depguard", Text: "import 'x' is not allowed from list 'wire-is-leaf': why"}
		i.Pos.Filename = file
		if _, err := classify([]lintIssue{i}); err == nil {
			t.Errorf("finding at %s was judged; a path outside the checked tree must fail the run", file)
		}
	}
}

func TestLintCacheBelongsToTheCheckedWorktree(t *testing.T) {
	a, b := lintCacheDir("/envs/1087/sparkwing"), lintCacheDir("/envs/1088/sparkwing")
	if a == b || !strings.HasPrefix(a, "/envs/1087/sparkwing/") {
		t.Errorf("lint caches %q and %q; each worktree needs its own, inside itself", a, b)
	}
}
