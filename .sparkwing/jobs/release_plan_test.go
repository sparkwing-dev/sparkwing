package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// releaseGateNodes are the nodes whose failure must stop a release. A
// release that reaches push-tag without all of them green has traded an
// unsatisfiable pipeline for an unsafe one.
var releaseGateNodes = []string{
	"validate-version",
	"check-clean-tree",
	"gate-pre-commit",
	"gate-pre-push",
	"gate-template-verify",
	"gate-release-lineage",
	"gate-schema-changelog",
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

func TestReleasePreviewExampleUsesTheReservedRunFlag(t *testing.T) {
	examples := (Release{}).Examples()
	if got := examples[len(examples)-1].Command; got != "sparkwing run release --sw-dry-run" {
		t.Fatalf("preview command = %q", got)
	}
}

// ancestors returns every node id that id depends on, directly or
// transitively.
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

func TestReleasePlanPinsSelfReplaceAfterTagPush(t *testing.T) {
	plan := releasePlan(t)

	if !ancestors(t, plan, "bump-self-replace")["push-tag"] {
		t.Error("bump-self-replace must depend on push-tag: the pin it writes names a version whose tag does not exist until push-tag creates it")
	}
	if ancestors(t, plan, "push-tag")["bump-self-replace"] {
		t.Error("push-tag must not depend on bump-self-replace, directly or transitively: that cycle of intent is what made the release unsatisfiable")
	}
}

func TestReleasePlanRestoresSelfReplaceWhenBumpFails(t *testing.T) {
	plan := releasePlan(t)

	bump := mustNode(t, plan, "bump-self-replace")
	if !bump.IsContinueOnError() {
		t.Error("bump-self-replace must be ContinueOnError so the restore still runs after a failed pin commit")
	}
	if bump.IsOptional() {
		t.Error("bump-self-replace must not be Optional: a failed pin has to fail the run")
	}
	if !ancestors(t, plan, "restore-self-replace")["bump-self-replace"] {
		t.Error("restore-self-replace must depend on bump-self-replace so it runs on the bump's failure path")
	}
}

func TestReleasePlanGatesBlockTagPush(t *testing.T) {
	plan := releasePlan(t)

	deps := ancestors(t, plan, "push-tag")
	for _, gate := range releaseGateNodes {
		if !deps[gate] {
			t.Errorf("push-tag must depend on %s; a release that skips a gate to reach the tag is unsafe", gate)
		}
		n := mustNode(t, plan, gate)
		if n.IsContinueOnError() || n.IsOptional() {
			t.Errorf("%s must block push-tag on failure, but is marked ContinueOnError/Optional", gate)
		}
	}
}

func TestReleasePlanDoesNotCommitChangelogBeforeIndependentGatesPass(t *testing.T) {
	deps := ancestors(t, releasePlan(t), "prepare-changelog")
	for _, gate := range []string{"gate-template-verify", "gate-release-lineage"} {
		if !deps[gate] {
			t.Errorf("prepare-changelog must depend on %s so a failed gate leaves HEAD unchanged", gate)
		}
	}
}

func TestReleasePlanRestoreDoesNotGateTagPush(t *testing.T) {
	plan := releasePlan(t)

	if ancestors(t, plan, "push-tag")["restore-self-replace"] {
		t.Error("push-tag must not depend on restore-self-replace: cleanup runs after the tag, never as a precondition for it")
	}
}
