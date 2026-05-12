package orchestrator

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestSplitExcludes_PrefixSplitsLists(t *testing.T) {
	inc, exc := SplitExcludes([]string{"main", "!canary", "release", "!hotfix"})
	if len(inc) != 2 || inc[0] != "main" || inc[1] != "release" {
		t.Errorf("include = %v", inc)
	}
	if len(exc) != 2 || exc[0] != "canary" || exc[1] != "hotfix" {
		t.Errorf("exclude = %v", exc)
	}
}

func TestParseSearch_IncludeAndExclude(t *testing.T) {
	got := ParseSearch("deploy -canary FRONTEND")
	if len(got.Include) != 2 {
		t.Errorf("include = %v", got.Include)
	}
	if got.Include[0] != "deploy" || got.Include[1] != "frontend" {
		t.Errorf("include casing/order: %v", got.Include)
	}
	if len(got.Exclude) != 1 || got.Exclude[0] != "canary" {
		t.Errorf("exclude = %v", got.Exclude)
	}
}

func TestParseLooseDate_RecognizedForms(t *testing.T) {
	cases := []string{"today", "yesterday", "24h", "7d", "5m", "2026-05-01"}
	for _, c := range cases {
		if _, err := ParseLooseDate(c); err != nil {
			t.Errorf("%q failed: %v", c, err)
		}
	}
}

func TestParseLooseDate_RejectsGarbage(t *testing.T) {
	if _, err := ParseLooseDate("not-a-date"); err == nil {
		t.Errorf("expected error for garbage input")
	}
}

func TestCompiledFilter_BranchAndExclude(t *testing.T) {
	f := CompiledFilter{Branches: []string{"main"}, BranchExcludes: []string{"canary"}}
	if !f.Matches(&store.Run{GitBranch: "main"}) {
		t.Error("main should match")
	}
	if f.Matches(&store.Run{GitBranch: "feature/x"}) {
		t.Error("feature/x should not match")
	}
	exc := CompiledFilter{BranchExcludes: []string{"canary"}}
	if exc.Matches(&store.Run{GitBranch: "canary"}) {
		t.Error("canary should be excluded")
	}
}

func TestCompiledFilter_SearchSemantics(t *testing.T) {
	f := CompiledFilter{Search: ParseSearch("deploy -canary")}
	if !f.Matches(&store.Run{Pipeline: "deploy-frontend", GitBranch: "main"}) {
		t.Error("deploy-frontend on main should match")
	}
	if f.Matches(&store.Run{Pipeline: "deploy-canary", GitBranch: "main"}) {
		t.Error("deploy-canary should be excluded")
	}
	if f.Matches(&store.Run{Pipeline: "test", GitBranch: "main"}) {
		t.Error("test pipeline (no 'deploy') should not match")
	}
}

func TestCompiledFilter_ErrorSubstringCaseInsensitive(t *testing.T) {
	f := CompiledFilter{ErrorSubstr: "Permission Denied"}
	if !f.Matches(&store.Run{Error: "AWS: permission denied for ssm:..."}) {
		t.Error("case-insensitive match expected")
	}
	if f.Matches(&store.Run{Error: "timeout"}) {
		t.Error("non-match should not pass")
	}
}

func TestCompiledFilter_DateWindows(t *testing.T) {
	now := time.Now()
	pastStart := now.Add(-time.Hour)
	pastEnd := now.Add(-30 * time.Minute)
	f := CompiledFilter{
		StartedAfter:   now.Add(-2 * time.Hour),
		FinishedBefore: now,
	}
	if !f.Matches(&store.Run{StartedAt: pastStart, FinishedAt: &pastEnd}) {
		t.Error("run within window should match")
	}
	// run StartedAt before window
	earlier := now.Add(-3 * time.Hour)
	if f.Matches(&store.Run{StartedAt: earlier, FinishedAt: &pastEnd}) {
		t.Error("run started before window should be excluded")
	}
	// run not finished
	if f.Matches(&store.Run{StartedAt: pastStart, FinishedAt: nil}) {
		t.Error("unfinished run should be excluded by finished-before")
	}
}

func TestCompiledFilter_HasAny(t *testing.T) {
	if (CompiledFilter{}).HasAny() {
		t.Error("zero value should be no-op")
	}
	if !(CompiledFilter{ErrorSubstr: "x"}).HasAny() {
		t.Error("ErrorSubstr should count")
	}
}

func TestCompiledFilter_StatusExcludes(t *testing.T) {
	f := CompiledFilter{StatusExcludes: []string{"success"}}
	if f.Matches(&store.Run{Status: "success"}) {
		t.Error("success should be excluded")
	}
	if !f.Matches(&store.Run{Status: "failed"}) {
		t.Error("failed should pass")
	}
}

func TestApplyClientFilters_FilteredAndOrderPreserved(t *testing.T) {
	t1 := time.Now().Add(-time.Hour)
	t2 := t1.Add(time.Minute)
	t3 := t2.Add(time.Minute)
	runs := []*store.Run{
		{ID: "a", GitBranch: "main", StartedAt: t1},
		{ID: "b", GitBranch: "feature/x", StartedAt: t2},
		{ID: "c", GitBranch: "main", StartedAt: t3},
	}
	got := applyClientFilters(runs, CompiledFilter{Branches: []string{"main"}})
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("order/filter wrong: %+v", got)
	}
}

func TestApplyClientFilters_EmptyFilterReturnsAll(t *testing.T) {
	runs := []*store.Run{{ID: "a"}, {ID: "b"}}
	got := applyClientFilters(runs, CompiledFilter{})
	if len(got) != 2 {
		t.Errorf("empty filter should return all: %+v", got)
	}
}
