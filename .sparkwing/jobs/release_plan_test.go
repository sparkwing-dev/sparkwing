package jobs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the whole local release. Every suite that once gated the tag runs in
// safety: hosted CI against the tagged source, so a node outside this list is
// safety: work the release does not need.
var releaseRecipe = []string{
	"check-clean-tree",
	"discover-version",
	"gate-schema-changelog",
	"gate-wire-changelog",
	"prepare-changelog",
	"push-tag",
	"validate-version",
}

func releasePlan(t *testing.T) *sparkwing.Plan {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatalf("seed fake repo root: %v", err)
	}
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(dir)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	plan := sparkwing.NewPlan()
	if err := (&Release{}).Plan(context.Background(), plan, ReleaseArgs{Version: "v0.99.0"}, sparkwing.RunContext{}); err != nil {
		t.Fatalf("build release plan: %v", err)
	}
	return plan
}

func mustNode(t *testing.T, plan *sparkwing.Plan, id string) *sparkwing.JobNode {
	t.Helper()
	n := plan.Job(id)
	if n == nil {
		t.Fatalf("release plan has no node %q", id)
	}
	return n
}

func ancestors(t *testing.T, plan *sparkwing.Plan, id string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	var walk func(string)
	walk = func(cur string) {
		for _, dep := range mustNode(t, plan, cur).DepIDs() {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			walk(dep)
		}
	}
	walk(id)
	return seen
}

func TestReleasePreviewExampleUsesTheReservedRunFlag(t *testing.T) {
	examples := (Release{}).Examples()
	if got := examples[len(examples)-1].Command; got != `SPARKWING_HOME="$(mktemp -d)" sparkwing run release --sw-dry-run` {
		t.Fatalf("preview command = %q", got)
	}
}

func TestReleasePlanIsTheTagRecipeAndNothingElse(t *testing.T) {
	plan := releasePlan(t)

	var got []string
	for _, n := range plan.Nodes() {
		got = append(got, n.ID())
	}
	sort.Strings(got)
	if !slices.Equal(got, releaseRecipe) {
		t.Fatalf("release plan nodes = %v, want %v; a release is a tag push, and every check of substance runs in hosted CI on the tagged source", got, releaseRecipe)
	}
}

func TestReleasePlanTagsOnlyAfterTheChangelogCommit(t *testing.T) {
	plan := releasePlan(t)

	deps := ancestors(t, plan, "push-tag")
	for _, need := range []string{"validate-version", "check-clean-tree", "prepare-changelog", "gate-schema-changelog", "gate-wire-changelog"} {
		if !deps[need] {
			t.Errorf("push-tag must depend on %s; the tag has to land on the tree that already carries the release notes", need)
		}
		n := mustNode(t, plan, need)
		if n.IsContinueOnError() || n.IsOptional() {
			t.Errorf("%s must block push-tag on failure, but is marked ContinueOnError/Optional", need)
		}
	}
}

func TestReleasePlanValidatesTheVersionBeforeTouchingTheTree(t *testing.T) {
	plan := releasePlan(t)

	deps := ancestors(t, plan, "prepare-changelog")
	for _, need := range []string{"validate-version", "check-clean-tree"} {
		if !deps[need] {
			t.Errorf("prepare-changelog must depend on %s so a refused version leaves HEAD unchanged", need)
		}
	}
	if ancestors(t, plan, "validate-version")["prepare-changelog"] {
		t.Error("validate-version must not wait for the commit it exists to precede")
	}
}

func TestRequireAheadOfNewestTag(t *testing.T) {
	cases := []struct {
		name    string
		version string
		newest  string
		wantErr bool
	}{
		{name: "first release", version: "v0.1.0", newest: ""},
		{name: "patch ahead", version: "v0.50.4", newest: "v0.50.3"},
		{name: "minor ahead", version: "v0.51.0", newest: "v0.50.3"},
		{name: "equal", version: "v0.50.3", newest: "v0.50.3", wantErr: true},
		{name: "behind", version: "v0.50.2", newest: "v0.50.3", wantErr: true},
		{name: "behind by minor", version: "v0.49.9", newest: "v0.50.0", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := requireAheadOfNewestTag(c.version, c.newest)
			if (err != nil) != c.wantErr {
				t.Fatalf("requireAheadOfNewestTag(%q, %q) = %v, wantErr %v", c.version, c.newest, err, c.wantErr)
			}
		})
	}
}
