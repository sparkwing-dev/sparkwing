package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func stepIDs(t *testing.T, p interface {
	Work(*sparkwing.Work) (*sparkwing.WorkStep, error)
},
) []string {
	t.Helper()
	w := inspectWork(t, p)
	var ids []string
	for _, s := range w.Steps {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	return ids
}

func nodeIDs(nodes []jobDocument) []string {
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID
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
	w := inspectWork(t, &PreCommit{})
	if w.FailurePolicy != sparkwing.FailFast {
		t.Error("pre-commit must fail fast: the author is holding a commit open")
	}
	for _, s := range w.Steps {
		if s.ID == budgetStepID {
			continue
		}
		if deps := s.Needs; len(deps) != 0 {
			t.Errorf("step %q waits on %v; every step here is sub-second, so serializing them only delays the verdict", s.ID, deps)
		}
	}
}

func TestPreCommitAdmitsAheadOfTheBroadGate(t *testing.T) {
	precommit := inspectPlan(t, func(precommit *sparkwing.Plan) error {
		return (&PreCommit{}).Plan(t.Context(), precommit, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-commit"})
	})
	gate := inspectPlan(t, func(gate *sparkwing.Plan) error {
		return (&Gate{}).Plan(t.Context(), gate, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "gate"})
	})
	if precommit.Priority <= gate.Priority {
		t.Fatalf("pre-commit priority %d, gate priority %d: a ten-second tier queued behind a ten-minute one is not a ten-second tier",
			precommit.Priority, gate.Priority)
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

func TestEveryDeclaredPipelineResolvesToItsConfiguredEntrypoint(t *testing.T) {
	t.Chdir(sourceTreeRoot())
	host := newTestHost(t, sparkwingtest.Options{})
	for _, p := range readPipelineConfig(t).Pipelines {
		if _, err := host.Plan(t.Context(), p.Entrypoint, nil); err != nil && strings.Contains(err.Error(), "pipeline is not registered") {
			t.Errorf("%s entrypoint %s is unregistered: %v", p.Name, p.Entrypoint, err)
		}
	}
	if _, err := host.Plan(t.Context(), "push-checks", nil); err == nil || !strings.Contains(err.Error(), "pipeline is not registered") {
		t.Fatalf("removed push-checks registration: %v", err)
	}
}

func TestPreCommitGofmtJudgesOnlyTheChange(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

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
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "internal", "bad.go"),
		"package internal\nfunc  Bad( )  int { return 3 }\n")
	gitAddAll(t, root)

	if err := runGofmtOnTheChange(ctx); err == nil {
		t.Error("gofmt passed a staged file it must reformat")
	}
}

// safety: the names exceed Linux's 128 KiB limit on one argument, and the one
// unformatted file comes last, so a run that judges only the first command
// line misses it.
func manyGoFiles(t *testing.T, root string) scopeFunc {
	t.Helper()
	dir := filepath.Join("internal", strings.Repeat("deeply_nested_package_", 3))
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	const count = 3000
	files := make([]string, 0, count+1)
	for i := range count {
		name := filepath.Join(dir, fmt.Sprintf("sound_file_with_a_long_name_%05d.go", i))
		writeGoFile(t, filepath.Join(root, name), "package internal\n")
		files = append(files, name)
	}
	last := filepath.Join(dir, "zz_unformatted.go")
	writeGoFile(t, filepath.Join(root, last), "package internal\nfunc  Bad( )  int { return 3 }\n")
	files = append(files, last)
	return func(context.Context, string, func([]string) []string) ([]string, string, error) {
		return files, fmt.Sprintf("%d Go file(s)", len(files)), nil
	}
}

func TestGofmtJudgesAChangeTooLargeForOneCommandLine(t *testing.T) {
	root := gateFixtureRepo(t)
	err := gofmtOverScope(context.Background(), manyGoFiles(t, root))
	if err == nil || !strings.Contains(err.Error(), "files need formatting") ||
		!strings.Contains(err.Error(), "zz_unformatted.go") || strings.Contains(err.Error(), "sound_file_with_a_long_name") {
		t.Fatalf("gofmt over a large change = %.300v, want it to name only zz_unformatted.go", err)
	}
}

func TestFormattersJudgeAChangeTooLargeForOneCommandLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in formatter is a POSIX script")
	}
	root := gateFixtureRepo(t)
	// perf: the real formatter costs about 0.2 s a file, so a stand-in that
	// reports the one unformatted file keeps this test under a second.
	bin := t.TempDir()
	writeGoFile(t, filepath.Join(bin, "golangci-lint"), "#!/bin/sh\nstatus=0\n"+
		"for f in \"$@\"; do case \"$f\" in *zz_unformatted.go) echo \"--- $f\"; status=1;; esac; done\nexit $status\n")
	if err := os.Chmod(filepath.Join(bin, "golangci-lint"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := formattersOverScope(context.Background(), manyGoFiles(t, root))
	if err == nil || !strings.Contains(err.Error(), "do not match the configured formatters") ||
		!strings.Contains(err.Error(), "zz_unformatted.go") || strings.Contains(err.Error(), "sound_file_with_a_long_name") {
		t.Fatalf("formatters over a large change = %.300v, want them to name only zz_unformatted.go", err)
	}
}
