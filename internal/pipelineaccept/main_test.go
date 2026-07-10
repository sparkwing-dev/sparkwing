package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/pipelinegen"
)

func TestExitCodeIsNonzeroOnRegression(t *testing.T) {
	if got := exitCode(pipelinegen.Report{Total: 6, Matched: 6}); got != 0 {
		t.Errorf("all matched: exitCode = %d, want 0", got)
	}
	if got := exitCode(pipelinegen.Report{Total: 6, Matched: 5}); got != 1 {
		t.Errorf("one unmatched: exitCode = %d, want 1", got)
	}
}

func TestMakeGeneratorSelectsByFlag(t *testing.T) {
	fsys, croot := pipelinegen.DefaultCorpus()

	gen, err := makeGenerator(options{generator: "fixture"}, fsys, croot)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if gen.Label() != "fixture" {
		t.Errorf("fixture generator label = %q", gen.Label())
	}

	gen, err = makeGenerator(options{generator: "command", command: "author.sh --model x"}, fsys, croot)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if gen.Label() != "command:author.sh --model x" {
		t.Errorf("command generator label = %q", gen.Label())
	}
}

func TestMakeGeneratorRejectsEmptyCommand(t *testing.T) {
	fsys, croot := pipelinegen.DefaultCorpus()
	if _, err := makeGenerator(options{generator: "command", command: "   "}, fsys, croot); err == nil {
		t.Error("expected an error when --generator=command with no --command")
	}
}

func TestMakeGeneratorRejectsUnknownKind(t *testing.T) {
	fsys, croot := pipelinegen.DefaultCorpus()
	if _, err := makeGenerator(options{generator: "wat"}, fsys, croot); err == nil {
		t.Error("expected an error for an unknown generator kind")
	}
}

func TestFilterSpecFindsOneAndErrorsOnMiss(t *testing.T) {
	specs := []pipelinegen.Spec{{Name: "minimal"}, {Name: "release"}}
	got, err := filterSpec(specs, "release")
	if err != nil {
		t.Fatalf("filter release: %v", err)
	}
	if len(got) != 1 || got[0].Name != "release" {
		t.Errorf("filter release = %+v", got)
	}
	if _, err := filterSpec(specs, "ghost"); err == nil {
		t.Error("expected an error filtering to a missing spec")
	}
}

func TestDropAntiPatternsKeepsOnlyExpectPass(t *testing.T) {
	specs := []pipelinegen.Spec{
		{Name: "good", Expect: pipelinegen.ExpectPass},
		{Name: "bad", Expect: pipelinegen.ExpectFail},
		{Name: "also-good", Expect: pipelinegen.ExpectPass},
	}
	kept := dropAntiPatterns(specs)
	if len(kept) != 2 {
		t.Fatalf("kept %d specs, want 2", len(kept))
	}
	for _, s := range kept {
		if s.Expect != pipelinegen.ExpectPass {
			t.Errorf("kept an anti-pattern spec: %s", s.Name)
		}
	}
}

func TestEmitJSONRoundTrips(t *testing.T) {
	rep := pipelinegen.Report{Generator: "fixture", Total: 1, Matched: 1, Passed: 1, PassExpected: 1, PassRate: 1}
	var buf bytes.Buffer
	if err := emit(&buf, "json", rep); err != nil {
		t.Fatal(err)
	}
	var back pipelinegen.Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("json did not round-trip: %v", err)
	}
	if back.Generator != "fixture" || back.PassRate != 1 {
		t.Errorf("round-tripped report = %+v", back)
	}
}

func TestEmitPrettyMarksRegression(t *testing.T) {
	rep := pipelinegen.Report{
		Generator: "fixture",
		Total:     1,
		Matched:   0,
		Results: []pipelinegen.SpecResult{{
			Name:   "minimal",
			Expect: pipelinegen.ExpectPass,
			Checks: []pipelinegen.CheckResult{{Name: pipelinegen.CheckLint, OK: false}},
		}},
	}
	var buf bytes.Buffer
	if err := emit(&buf, "pretty", rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "REGRESSION") {
		t.Errorf("pretty output should flag the regression:\n%s", out)
	}
	if !strings.Contains(out, "[lint✗]") {
		t.Errorf("pretty output should name the failed check:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("pretty output should end with FAIL:\n%s", out)
	}
}

func TestEmitRejectsUnknownFormat(t *testing.T) {
	if err := emit(&bytes.Buffer{}, "yaml", pipelinegen.Report{}); err == nil {
		t.Error("expected an error for an unknown output format")
	}
}

// TestRunFixtureCorpusEndToEnd exercises the runner over the whole fixture
// corpus through the real gofmt/compile/vet/explain/lint bar: it builds
// sparkwing and compiles a project per spec, so it is opt-in via
// SPARKWING_PIPELINEGEN_E2E=1 (and skipped in -short). Every spec must
// agree with its expectation, and the pass specs must clear the two oracles
// this runner adds (format, vet).
func TestRunFixtureCorpusEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end runner skipped in -short mode")
	}
	if os.Getenv("SPARKWING_PIPELINEGEN_E2E") != "1" {
		t.Skip("set SPARKWING_PIPELINEGEN_E2E=1 to run the fixture corpus end-to-end")
	}

	rep, err := run(context.Background(), options{output: "json", generator: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Matched != rep.Total {
		t.Errorf("%d/%d specs disagreed with expectation", rep.Total-rep.Matched, rep.Total)
	}
	if rep.PassRate != 1.0 {
		t.Errorf("pass-rate = %.2f over %d idiomatic specs, want 1.0", rep.PassRate, rep.PassExpected)
	}
	for _, r := range rep.Results {
		if r.Expect != pipelinegen.ExpectPass {
			continue
		}
		if !hasOKCheck(r.Checks, pipelinegen.CheckFormat) {
			t.Errorf("%s: missing a passing format check: %+v", r.Name, r.Checks)
		}
		if !hasOKCheck(r.Checks, pipelinegen.CheckVet) {
			t.Errorf("%s: missing a passing vet check: %+v", r.Name, r.Checks)
		}
	}
}

func hasOKCheck(checks []pipelinegen.CheckResult, name pipelinegen.CheckName) bool {
	for _, c := range checks {
		if c.Name == name {
			return c.OK
		}
	}
	return false
}
