package pipelinegen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
