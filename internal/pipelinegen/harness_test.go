package pipelinegen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stubGenerator returns canned source (or an error) per spec name.
type stubGenerator struct {
	src  map[string]string
	errs map[string]error
}

func (stubGenerator) Label() string { return "stub" }

func (g stubGenerator) Generate(_ context.Context, spec Spec) (string, error) {
	if err := g.errs[spec.Name]; err != nil {
		return "", err
	}
	return g.src[spec.Name], nil
}

// stubScorer fails the named specs (one failing lint check) and passes
// the rest, so a run is fully deterministic without building anything.
type stubScorer struct{ failing map[string]bool }

func (s stubScorer) Score(_ context.Context, spec Spec, _ string) ([]CheckResult, error) {
	checks := []CheckResult{{Name: CheckCompile, OK: true}, {Name: CheckExplain, OK: true}}
	checks = append(checks, CheckResult{Name: CheckLint, OK: !s.failing[spec.Name]})
	return checks, nil
}

// revisingGenerator emits bad source until it has been given feedback
// reviseAfter times, then emits good source. It records the feedback it
// was handed so a test can assert the oracle output actually reaches the
// author.
type revisingGenerator struct {
	reviseAfter int
	rounds      int
	gotFeedback []string
}

func (g *revisingGenerator) Label() string { return "revising" }

func (g *revisingGenerator) Generate(context.Context, Spec) (string, error) {
	g.rounds = 0
	return "bad", nil
}

func (g *revisingGenerator) Revise(_ context.Context, _ Spec, _, feedback string) (string, error) {
	g.rounds++
	g.gotFeedback = append(g.gotFeedback, feedback)
	if g.rounds >= g.reviseAfter {
		return "good", nil
	}
	return "bad", nil
}

// sourceScorer passes only the exact source "good".
type sourceScorer struct{}

func (sourceScorer) Score(_ context.Context, _ Spec, source string) ([]CheckResult, error) {
	ok := source == "good"
	return []CheckResult{{Name: CheckLint, OK: ok, Detail: "lint: bad plan"}}, nil
}

func TestRunWithRevisionRescuesAFailingGeneration(t *testing.T) {
	specs := []Spec{{Name: "s", Expect: ExpectPass}}
	gen := &revisingGenerator{reviseAfter: 1}

	rep := RunWith(context.Background(), specs, gen, sourceScorer{}, RunOptions{Revise: 1})
	if rep.Passed != 1 {
		t.Fatalf("revision should have rescued the generation: %+v", rep.Results[0])
	}
	if got := rep.Results[0].Revisions; got != 1 {
		t.Errorf("Revisions = %d, want 1", got)
	}
	if len(gen.gotFeedback) != 1 || !strings.Contains(gen.gotFeedback[0], "lint: bad plan") {
		t.Errorf("reviser did not receive the oracle detail: %q", gen.gotFeedback)
	}
	if rep.Stats[0].Passed != 1 || rep.Stats[0].FirstTry != 0 {
		t.Errorf("a rescued generation passed but not first-try: %+v", rep.Stats[0])
	}
}

func TestRunWithRevisionBudgetIsRespected(t *testing.T) {
	specs := []Spec{{Name: "s", Expect: ExpectPass}}
	gen := &revisingGenerator{reviseAfter: 2}

	rep := RunWith(context.Background(), specs, gen, sourceScorer{}, RunOptions{Revise: 1})
	if rep.Passed != 0 {
		t.Error("a generation needing two rounds must not pass with a budget of one")
	}
	if gen.rounds != 1 {
		t.Errorf("reviser called %d times, want 1", gen.rounds)
	}
}

func TestRunWithoutReviseStaysOneShot(t *testing.T) {
	specs := []Spec{{Name: "s", Expect: ExpectPass}}
	gen := &revisingGenerator{reviseAfter: 1}

	rep := RunWith(context.Background(), specs, gen, sourceScorer{}, RunOptions{})
	if rep.Passed != 0 || gen.rounds != 0 {
		t.Errorf("default options must not revise: passed=%d rounds=%d", rep.Passed, gen.rounds)
	}
}

// An expect=fail spec encodes an anti-pattern on purpose, so feeding it
// the linter's complaint would ask it to stop being the thing under test.
func TestRunWithDoesNotReviseAntiPatternSpecs(t *testing.T) {
	specs := []Spec{{Name: "bad", Expect: ExpectFail}}
	gen := &revisingGenerator{reviseAfter: 1}

	rep := RunWith(context.Background(), specs, gen, sourceScorer{}, RunOptions{Revise: 2})
	if gen.rounds != 0 {
		t.Errorf("anti-pattern spec was revised %d times, want 0", gen.rounds)
	}
	if !rep.Results[0].Matched {
		t.Error("an anti-pattern spec that failed the bar should match its expectation")
	}
}

func TestRunWithRepeatSamplesEachSpec(t *testing.T) {
	specs := []Spec{{Name: "good", Expect: ExpectPass}}
	gen := stubGenerator{src: map[string]string{"good": "x"}}

	rep := RunWith(context.Background(), specs, gen, stubScorer{}, RunOptions{Repeat: 3})
	if rep.Total != 3 || len(rep.Results) != 3 {
		t.Fatalf("Repeat=3 should score 3 attempts, got %d", rep.Total)
	}
	if rep.Repeat != 3 {
		t.Errorf("Repeat = %d, want 3", rep.Repeat)
	}
	if len(rep.Stats) != 1 || rep.Stats[0].Attempts != 3 || rep.Stats[0].MatchRate != 1.0 {
		t.Errorf("stats should roll 3 attempts into one row: %+v", rep.Stats)
	}
	for i, r := range rep.Results {
		if r.Attempt != i+1 {
			t.Errorf("result %d has Attempt %d", i, r.Attempt)
		}
	}
}

func TestSaveSourceKeepsEachRevisionRound(t *testing.T) {
	dir := t.TempDir()
	first, err := saveSource(dir, "spec", 2, 0, "package jobs // first")
	if err != nil {
		t.Fatal(err)
	}
	revised, err := saveSource(dir, "spec", 2, 1, "package jobs // revised")
	if err != nil {
		t.Fatal(err)
	}
	if first == revised {
		t.Fatal("a revision must not overwrite the attempt it revised")
	}
	if filepath.Base(first) != "spec-2.go" {
		t.Errorf("first = %q", filepath.Base(first))
	}
	if filepath.Base(revised) != "spec-2-r1.go" {
		t.Errorf("revised = %q", filepath.Base(revised))
	}
	body, err := os.ReadFile(revised)
	if err != nil || !strings.Contains(string(body), "revised") {
		t.Errorf("revised source not written: %v %q", err, body)
	}
}

func TestRunAggregatesPassRateAndMatched(t *testing.T) {
	specs := []Spec{
		{Name: "good", Expect: ExpectPass},
		{Name: "bad", Expect: ExpectFail},
	}
	gen := stubGenerator{src: map[string]string{"good": "x", "bad": "y"}}
	scorer := stubScorer{failing: map[string]bool{"bad": true}}

	rep := Run(context.Background(), specs, gen, scorer)
	if rep.Total != 2 || rep.PassExpected != 1 || rep.Passed != 1 {
		t.Fatalf("counts: %+v", rep)
	}
	if rep.Matched != 2 {
		t.Errorf("Matched = %d, want 2 (both specs agreed with expectation)", rep.Matched)
	}
	if rep.PassRate != 1.0 {
		t.Errorf("PassRate = %v, want 1.0", rep.PassRate)
	}
}

func TestRunCatchesRegressionWhenGoodSpecFails(t *testing.T) {
	specs := []Spec{{Name: "good", Expect: ExpectPass}}
	gen := stubGenerator{src: map[string]string{"good": "x"}}
	scorer := stubScorer{failing: map[string]bool{"good": true}}

	rep := Run(context.Background(), specs, gen, scorer)
	if rep.Passed != 0 || rep.Matched != 0 || rep.PassRate != 0 {
		t.Fatalf("a failing good spec must be unmatched: %+v", rep)
	}
}

func TestRunGenerationErrorMatchesOnlyExpectFail(t *testing.T) {
	specs := []Spec{
		{Name: "broken-fail", Expect: ExpectFail},
		{Name: "broken-pass", Expect: ExpectPass},
	}
	gen := stubGenerator{errs: map[string]error{
		"broken-fail": fmt.Errorf("boom"),
		"broken-pass": fmt.Errorf("boom"),
	}}
	rep := Run(context.Background(), specs, gen, stubScorer{})
	byName := map[string]SpecResult{}
	for _, r := range rep.Results {
		byName[r.Name] = r
	}
	if !byName["broken-fail"].Matched {
		t.Error("a generation error on an expect=fail spec should match")
	}
	if byName["broken-pass"].Matched {
		t.Error("a generation error on an expect=pass spec should not match")
	}
	for _, r := range rep.Results {
		if r.GenError == "" {
			t.Errorf("%q: expected a recorded generation error", r.Name)
		}
	}
}

// TestEvalHarnessEndToEnd is the acceptance instrument: it generates the
// whole corpus (fixture-backed) and scores each through the real
// compile + `pipeline explain` + `pipeline lint` bar, asserting every
// spec agrees with its expectation and the idiomatic specs all pass. It
// builds the sparkwing binary and compiles a project per spec, so it is
// opt-in via SPARKWING_PIPELINEGEN_E2E=1 (and skipped in -short).
func TestEvalHarnessEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end harness skipped in -short mode")
	}
	if os.Getenv("SPARKWING_PIPELINEGEN_E2E") != "1" {
		t.Skip("set SPARKWING_PIPELINEGEN_E2E=1 to run the compile+explain+lint corpus")
	}

	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	repoRoot := string(root[:len(root)-1])
	base := filepath.Join(repoRoot, ".sparkwing")

	bin := filepath.Join(t.TempDir(), "sparkwing")
	build := exec.Command("go", "build", "-o", bin, "./cmd/sparkwing")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sparkwing: %v\n%s", err, out)
	}

	fsys, croot := DefaultCorpus()
	specs, err := LoadCorpus(fsys, croot)
	if err != nil {
		t.Fatal(err)
	}
	gen := FixtureGenerator{FS: fsys, Root: croot}
	scorer := NewProjectScorer(bin, base)
	rep := Run(context.Background(), specs, gen, scorer)

	pretty, _ := json.MarshalIndent(rep, "", "  ")
	t.Logf("pipelinegen report:\n%s", pretty)

	if rep.Matched != rep.Total {
		t.Errorf("%d/%d specs disagreed with expectation", rep.Total-rep.Matched, rep.Total)
	}
	if rep.PassRate != 1.0 {
		t.Errorf("pass-rate = %.2f over %d idiomatic specs, want 1.0", rep.PassRate, rep.PassExpected)
	}
	for _, r := range rep.Results {
		if r.Expect == ExpectFail && r.Passed {
			t.Errorf("anti-pattern spec %q passed the bar; the linter/explain failed to reject it", r.Name)
		}
	}
}
