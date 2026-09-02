package jobs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestReleaseTemplateVerificationAllowsSerializedAdmission(t *testing.T) {
	if templateVerifyReleaseTimeout < time.Hour {
		t.Fatalf("template verification timeout = %s, want at least 1h", templateVerifyReleaseTimeout)
	}
}

var releaseGateNodes = []string{
	"validate-version",
	"check-clean-tree",
	"gate-contracts",
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
	if got := examples[len(examples)-1].Command; got != `SPARKWING_HOME="$(mktemp -d)" sparkwing run release --sw-dry-run` {
		t.Fatalf("preview command = %q", got)
	}
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

func TestReleasePlanSerializesTemplateVerificationAfterLocalGates(t *testing.T) {
	plan := releasePlan(t)
	deps := ancestors(t, plan, "gate-template-verify")
	for _, gate := range []string{"gate-pre-commit", "gate-pre-push"} {
		if !deps[gate] {
			t.Errorf("gate-template-verify must depend on %s", gate)
		}
	}
	hints := mustNode(t, plan, "gate-template-verify").ResourceHints()
	if hints == nil || hints.Cores != 0.5 {
		t.Fatalf("gate-template-verify resources = %+v, want 0.5 coordinator cores", hints)
	}
}

func TestReleasePlanRunsContractPreflightBeforeTheRootGoSuite(t *testing.T) {
	plan := releasePlan(t)

	if !ancestors(t, plan, "gate-pre-commit")["gate-contracts"] {
		t.Error("gate-pre-commit must depend on gate-contracts: a contract failure has to return before the longest local suite runs")
	}
	if ancestors(t, plan, "gate-contracts")["gate-pre-commit"] {
		t.Error("gate-contracts must not depend on gate-pre-commit, directly or transitively: that puts it back behind the suite it exists to precede")
	}
	if ancestors(t, plan, "gate-contracts")["gate-pre-push"] || ancestors(t, plan, "gate-contracts")["gate-template-verify"] {
		t.Error("gate-contracts must not depend on the expensive gates")
	}
}

func TestReleaseContractPreflightRequiresEveryNamedCheckToPass(t *testing.T) {
	check := contractCheck{
		Label:   "contracts",
		Command: "go test -v ./cmd/sparkwing -run " + contractTestPattern([]string{"TestAlpha", "TestBeta"}),
		Tests:   []string{"TestAlpha", "TestBeta"},
	}

	full := "--- PASS: TestAlpha (0.01s)\n--- PASS: TestBeta (0.00s)\nok  cmd/sparkwing 0.3s\n"
	if err := requireContractTestsPassed(check, full); err != nil {
		t.Fatalf("a run that passed every named check was rejected: %v", err)
	}

	err := requireContractTestsPassed(check, "--- PASS: TestAlpha (0.01s)\nok  cmd/sparkwing 0.3s\n")
	if err == nil || !strings.Contains(err.Error(), "TestBeta") || strings.Contains(err.Error(), "TestAlpha\n") {
		t.Fatalf("a vanished check = %v, want a refusal naming only TestBeta", err)
	}

	if err := requireContractTestsPassed(check, "ok  cmd/sparkwing 0.3s [no tests to run]\n"); err == nil {
		t.Fatal("a run that matched nothing passed the preflight")
	}

	if err := requireContractTestsPassed(check, "--- PASS: TestAlphaExtended (0.01s)\n--- PASS: TestBeta (0.0s)\n"); err == nil {
		t.Fatal("a check whose name is only a prefix of another satisfied the preflight")
	}
}

func TestReleaseContractPreflightNamesTheContractsItClaims(t *testing.T) {
	checks := releaseContractChecks()
	if len(checks) != 2 {
		t.Fatalf("preflight has %d checks, want the docs mirror and the contract set", len(checks))
	}
	named := checks[1]
	for _, want := range releaseContractTests {
		if !strings.Contains(named.Command, want) {
			t.Errorf("the -run pattern does not name %s", want)
		}
		if !slices.Contains(named.Tests, want) {
			t.Errorf("the required-pass list does not name %s", want)
		}
	}
	for _, want := range []string{"EnvironmentVariable", "Registry", "Help", "Docs"} {
		if !strings.Contains(strings.Join(releaseContractTests, " "), want) {
			t.Errorf("the contract set covers no %s check, but the label claims one", want)
		}
	}
}

func TestReleaseAlwaysRequestsAnExhaustiveTemplateProof(t *testing.T) {
	if !releaseTemplateVerifyArgs.Exhaustive {
		t.Error("the release gate must request an exhaustive template proof; a recorded proof shortens local iteration, never the tag boundary")
	}
}

func TestReleasePlanSerializesPrePushAfterPreCommit(t *testing.T) {
	deps := ancestors(t, releasePlan(t), "gate-pre-push")
	if !deps["gate-pre-commit"] {
		t.Error("gate-pre-push must depend on gate-pre-commit")
	}
}

func TestReleasePlanRestoreDoesNotGateTagPush(t *testing.T) {
	plan := releasePlan(t)

	if ancestors(t, plan, "push-tag")["restore-self-replace"] {
		t.Error("push-tag must not depend on restore-self-replace: cleanup runs after the tag, never as a precondition for it")
	}
}
