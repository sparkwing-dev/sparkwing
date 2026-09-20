package jobs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the whole local release: the tag recipe plus the release cut's own
// safety: check class. The heavier suites run in hosted CI against the tagged
// safety: source, so a node outside this list is work the release does not need.
var releaseRecipe = []string{
	"check-clean-tree",
	"discover-version",
	"gate-schema-changelog",
	"gate-wire-changelog",
	"prepare-changelog",
	"push-tag",
	"release-cut-checks",
	"validate-version",
}

func releasePlan(t *testing.T) *sparkwing.Plan {
	t.Helper()
	dir := t.TempDir()
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
	for _, need := range []string{"validate-version", "check-clean-tree", "prepare-changelog", "gate-schema-changelog", "gate-wire-changelog", "release-cut-checks"} {
		if !deps[need] {
			t.Errorf("push-tag must depend on %s; the tag has to land on the tree that already carries the release notes", need)
		}
		n := mustNode(t, plan, need)
		if n.IsContinueOnError() || n.IsOptional() {
			t.Errorf("%s must block push-tag on failure, but is marked ContinueOnError/Optional", need)
		}
	}
}

func TestReleaseCutChecksJudgeTheTreeBeforeTheChangelogCommit(t *testing.T) {
	plan := releasePlan(t)

	if !ancestors(t, plan, "prepare-changelog")["release-cut-checks"] {
		t.Error("prepare-changelog must wait on release-cut-checks; a commit landing under a running lint or suite changes the tree it is reading")
	}
	if ancestors(t, plan, "release-cut-checks")["prepare-changelog"] {
		t.Error("the cut's checks wait on the commit they exist to precede")
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

func TestReleaseTagGrammarIsTheOneGrammar(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(root, "bin", "check-release-tag-order.sh"))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	expression := strings.TrimPrefix(strings.TrimSuffix(releaseTagGrammar.String(), "$"), "^")
	if !strings.Contains(string(script), expression) {
		t.Errorf("bin/check-release-tag-order.sh does not carry the grammar %s", expression)
	}
	if !strings.Contains(string(workflow), expression) {
		t.Errorf(".github/workflows/release.yaml does not carry the grammar %s", expression)
	}
}

func TestReleaseTagGrammarAndThePreV1Lock(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		wantShape bool
		wantLine  bool
		wantCut   bool
	}{
		{name: "stable", version: "v0.50.4", wantShape: true, wantLine: true, wantCut: true},
		{name: "prerelease", version: "v0.50.4-rc.1", wantShape: true, wantLine: true},
		{name: "build metadata", version: "v0.50.4+deadbeef"},
		{name: "leading zeros", version: "v0.50.04"},
		{name: "two fields", version: "v0.50"},
		{name: "no v prefix", version: "0.50.4"},
		{name: "retracted v1 tombstone", version: "v1.6.1", wantShape: true},
		{name: "v1 and above", version: "v1.0.0", wantShape: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isReleaseTagShape(c.version); got != c.wantShape {
				t.Errorf("isReleaseTagShape(%q) = %v, want %v", c.version, got, c.wantShape)
			}
			if got := validateReleaseVersion(c.version) == nil; got != c.wantCut {
				t.Errorf("validateReleaseVersion(%q) accepted = %v, want %v", c.version, got, c.wantCut)
			}
			inHighest := highestReleaseTag([]string{c.version}) == c.version
			if inHighest != c.wantLine {
				t.Errorf("highestReleaseTag counts %q = %v, want %v; the newest tag must be what the workflow calls the newest tag", c.version, inHighest, c.wantLine)
			}
		})
	}
}

func TestPreviousReleaseTagIsNotTheReleaseBeingCut(t *testing.T) {
	tags := []string{"v0.50.2", "v0.50.3", "v0.50.4"}
	if got := highestReleaseTag(tags); got != "v0.50.4" {
		t.Fatalf("highestReleaseTag = %q, want v0.50.4", got)
	}
	trimmed := slices.DeleteFunc(slices.Clone(tags), func(t string) bool { return t == "v0.50.4" })
	if got := highestReleaseTag(trimmed); got != "v0.50.3" {
		t.Fatalf("previous tag = %q, want v0.50.3; the schema and wire gates would diff a release against itself", got)
	}
}
