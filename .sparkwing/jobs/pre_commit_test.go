package jobs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func stepIDs(t *testing.T, p interface {
	Work(*sparkwing.Work) (*sparkwing.WorkStep, error)
},
) []string {
	t.Helper()
	w := sparkwing.NewWork()
	if _, err := p.Work(w); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range w.Steps() {
		ids = append(ids, s.ID())
	}
	slices.Sort(ids)
	return ids
}

func nodeIDs(nodes []*sparkwing.JobNode) []string {
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID()
	}
	return ids
}

func TestPreCommitRunsTheSourcePolicyStepsAndNothingElse(t *testing.T) {
	want := []string{
		"budget", "changelog-links", "comments", "docs-mirror", "em-dashes", "formatters",
		"gofmt", "home-resolution", "test-sleeps", "tracked-binaries", "tracker-ids",
	}
	if got := stepIDs(t, &PreCommit{}); !slices.Equal(got, want) {
		t.Fatalf("pre-commit steps = %v, want %v", got, want)
	}
}

func TestPreCommitRunsNothingThatCompilesLinksOrLeavesTheCheckout(t *testing.T) {
	got := stepIDs(t, &PreCommit{})
	for _, heavy := range []string{
		"vet", "build", "test", "lint", "race-touched", "store-postgres",
		"frontend-unit", "frontend-lint", "frontend-build", "frontend-browser",
	} {
		if slices.Contains(got, heavy) {
			t.Errorf("pre-commit runs %q; the tier promises a verdict in seconds, and every one of these compiles, links, or downloads", heavy)
		}
	}
}

func TestPreCommitStepsAllRunInParallel(t *testing.T) {
	w := sparkwing.NewWork()
	if _, err := (&PreCommit{}).Work(w); err != nil {
		t.Fatal(err)
	}
	if w.ParallelFailurePolicy() != sparkwing.FailFast {
		t.Error("pre-commit must fail fast: the author is holding a commit open")
	}
	for _, s := range w.Steps() {
		if s.ID() == budgetStepID {
			continue
		}
		if deps := s.DepIDs(); len(deps) != 0 {
			t.Errorf("step %q waits on %v; every step here is sub-second, so serializing them only delays the verdict", s.ID(), deps)
		}
	}
}

func TestPreCommitAdmitsAheadOfTheBroadGate(t *testing.T) {
	precommit := sparkwing.NewPlan()
	if err := (&PreCommit{}).Plan(t.Context(), precommit, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-commit"}); err != nil {
		t.Fatal(err)
	}
	gate := sparkwing.NewPlan()
	if err := (&Gate{}).Plan(t.Context(), gate, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "gate"}); err != nil {
		t.Fatal(err)
	}
	if precommit.PriorityValue() <= gate.PriorityValue() {
		t.Fatalf("pre-commit priority %d, gate priority %d: a ten-second tier queued behind a ten-minute one is not a ten-second tier",
			precommit.PriorityValue(), gate.PriorityValue())
	}
	hints := precommit.ResourceHints()
	if hints == nil || hints.Cores <= 0 || hints.Cores > 1 {
		t.Fatalf("pre-commit reserved cores = %#v, want a pin of at most one so it fits beside a running gate", hints)
	}
}

func TestGateStillRunsEveryStepTheHookTiersAlsoRun(t *testing.T) {
	gate := stepIDs(t, &Gate{})
	// safety: the gate's whole-module vet and build are what the push tier
	// runs over the touched packages alone.
	broader := map[string]string{
		"build-touched": "build",
		"vet-touched":   "vet",
		"lint-touched":  "lint",
	}
	for tier, ids := range map[string][]string{
		"pre-commit": stepIDs(t, &PreCommit{}),
		"pre-push":   stepIDs(t, &PrePush{}),
	} {
		for _, id := range ids {
			// safety: the budget is each tier's own verdict on its own class,
			// not a check the broad tier owes a copy of.
			if id == budgetStepID {
				continue
			}
			if slices.Contains(gate, id) || slices.Contains(gate, broader[id]) {
				continue
			}
			t.Errorf("gate dropped %q: the broad tier is the one hosted CI runs, so a step that lives only at %s is unenforced on a pull request", id, tier)
		}
	}
}

type pipelineConfig struct {
	Pipelines []struct {
		Name       string         `yaml:"name"`
		Entrypoint string         `yaml:"entrypoint"`
		On         map[string]any `yaml:"on"`
	} `yaml:"pipelines"`
}

func readPipelineConfig(t *testing.T) pipelineConfig {
	t.Helper()
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path")
	}
	data, err := os.ReadFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg pipelineConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestEachGitHookTierIsDeclaredExactlyOnce(t *testing.T) {
	byTrigger := map[string][]string{}
	for _, p := range readPipelineConfig(t).Pipelines {
		for event := range p.On {
			byTrigger[event] = append(byTrigger[event], p.Name)
		}
	}
	for event, want := range map[string]string{"pre_commit": "pre-commit", "pre_push": "pre-push"} {
		got := byTrigger[event]
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s is declared by %v, want exactly [%s]: a second pipeline on the same trigger doubles what the hook costs", event, got, want)
		}
	}
}

func TestEveryDeclaredPipelineResolvesToARegisteredName(t *testing.T) {
	for _, p := range readPipelineConfig(t).Pipelines {
		if _, ok := sparkwing.Lookup(p.Name); !ok {
			t.Errorf("%s is declared in sparkwing.yaml but no job registers it", p.Name)
		}
	}
	for _, gone := range []string{"push-checks"} {
		if _, ok := sparkwing.Lookup(gone); ok {
			t.Errorf("%q is still registered; the tiers are pre-commit, pre-push, gate and pre-release, and a surviving alias keeps the old meaning alive", gone)
		}
	}
}

func TestPreCommitGofmtJudgesOnlyTheChange(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := grantedCtx(context.Background())

	writeGoFile(t, filepath.Join(root, "internal", "legacy.go"),
		"package internal\nfunc  Legacy( )  int { return 1 }\n")
	gitCommitAll(t, root, "history the commit does not touch")

	writeGoFile(t, filepath.Join(root, "internal", "clean.go"),
		"package internal\n\nfunc Clean() int { return 2 }\n")
	gitAddAll(t, root)

	if err := runGofmtOnTheChange(ctx); err != nil {
		t.Errorf("gofmt charged the commit for untouched history: %v", err)
	}
	if err := runGofmt(ctx); err == nil {
		t.Error("the whole-tree form passed the same tree, so this test proves nothing about scope")
	}
}

func TestPreCommitGofmtRefusesWhatTheStagedChangeIntroduces(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := grantedCtx(context.Background())

	writeGoFile(t, filepath.Join(root, "internal", "bad.go"),
		"package internal\nfunc  Bad( )  int { return 3 }\n")
	gitAddAll(t, root)

	if err := runGofmtOnTheChange(ctx); err == nil {
		t.Error("gofmt passed a staged file it must reformat")
	}
}
