package pipelinelint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// lintSource writes src as a single .go file in a temp dir and returns
// the findings AnalyzeSource produces over it.
func lintSource(t *testing.T, src string) []Finding {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pipeline.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := AnalyzeSource(dir)
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	return findings
}

func countRule(findings []Finding, rule string) int {
	n := 0
	for _, f := range findings {
		if f.Rule == rule {
			n++
		}
	}
	return n
}

const fixtureHeader = `package jobs

import (
	"context"
	"os"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type P struct{ sparkwing.Base }

`

func TestPlanIO_FlagsShellAndFileCallsInPlanBody(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Bash(context.Background(), "echo hi").Run()
	os.ReadFile("/etc/hosts")
	return nil
}`
	findings := lintSource(t, src)
	if got := countRule(findings, RulePlanIO); got != 2 {
		t.Fatalf("plan-io findings = %d, want 2: %+v", got, findings)
	}
}

func TestPlanIO_AllowsIOInsideJobAndSkipIfClosures(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", func(ctx context.Context) error {
		sparkwing.Bash(ctx, "make build").Run()
		os.ReadFile("/tmp/x")
		return nil
	}).SkipIf(func(ctx context.Context) bool {
		return os.Getenv("SKIP") == "1"
	})
	return nil
}`
	findings := lintSource(t, src)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for I/O inside closures, got: %+v", findings)
	}
}

func TestPlanRuntimeBranch_FlagsEnvAndRuntimeAndIsLocal(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	if os.Getenv("ENV") == "prod" {
		sparkwing.Job(plan, "a", nil)
	}
	if runtime.GOOS == "linux" {
		sparkwing.Job(plan, "b", nil)
	}
	if rc.IsLocal() {
		sparkwing.Job(plan, "c", nil)
	}
	return nil
}`
	findings := lintSource(t, src)
	if got := countRule(findings, RulePlanRuntimeBranch); got != 3 {
		t.Fatalf("plan-runtime-branch findings = %d, want 3: %+v", got, findings)
	}
}

func TestRunnerLabel_FlagsBlankLabel(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "a", nil).Requires("gpu", "  ")
	return nil
}`
	findings := lintSource(t, src)
	if got := countRule(findings, RuleRunnerLabel); got != 1 {
		t.Fatalf("runner-label findings = %d, want 1: %+v", got, findings)
	}
}

func TestRunnerLabel_FlagsInlineWithRequires(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "a", nil).Inline().Requires("gpu")
	return nil
}`
	findings := lintSource(t, src)
	if got := countRule(findings, RuleRunnerLabel); got != 1 {
		t.Fatalf("runner-label findings = %d, want 1: %+v", got, findings)
	}
}

func TestRunnerLabel_CleanRequiresIsAllowed(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "a", nil).Requires("gpu", "arch=arm64")
	return nil
}`
	findings := lintSource(t, src)
	if got := countRule(findings, RuleRunnerLabel); got != 0 {
		t.Fatalf("runner-label findings = %d, want 0: %+v", got, findings)
	}
}

func TestUnusedRef_FlagsBlankAssignAndBareStatement(t *testing.T) {
	src := fixtureHeader + `type Out struct{ Tag string }

func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", nil)
	_ = sparkwing.RefTo[Out](build)
	sparkwing.RefTo[Out](build)
	return nil
}`
	findings := lintSource(t, src)
	if got := countRule(findings, RuleUnusedRef); got != 2 {
		t.Fatalf("unused-ref findings = %d, want 2: %+v", got, findings)
	}
}

func TestUnusedRef_ConsumedRefIsAllowed(t *testing.T) {
	src := fixtureHeader + `type Out struct{ Tag string }

func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", nil)
	ref := sparkwing.RefTo[Out](build)
	sparkwing.Job(plan, "deploy", &deployer{Build: ref}).Needs(build)
	return nil
}

type deployer struct {
	sparkwing.Base
	Build sparkwing.Ref[Out]
}`
	findings := lintSource(t, src)
	if got := countRule(findings, RuleUnusedRef); got != 0 {
		t.Fatalf("unused-ref findings = %d, want 0: %+v", got, findings)
	}
}

func TestAnalyzeSource_IgnoresNonPipelineMethodsNamedPlan(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(s string) error {
	os.ReadFile(s)
	return nil
}`
	findings := lintSource(t, src)
	if len(findings) != 0 {
		t.Fatalf("a non-pipeline Plan(string) must not be analyzed, got: %+v", findings)
	}
}

func TestAnalyzeSource_TagsFindingWithReceiverType(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	os.ReadFile("/x")
	return nil
}`
	findings := lintSource(t, src)
	if len(findings) != 1 || findings[0].Pipeline != "P" {
		t.Fatalf("finding should be tagged with receiver type P: %+v", findings)
	}
	if findings[0].Line == 0 || findings[0].File == "" {
		t.Fatalf("finding should carry a source location: %+v", findings[0])
	}
}

func TestGuardMisuse_RequireAndRejectSameToken(t *testing.T) {
	cfg := &pipelines.Config{Pipelines: []pipelines.Pipeline{{
		Name:       "deploy",
		Entrypoint: "Deploy",
		Guards: pipelines.Guards{
			Require: []string{"profile:controller"},
			Reject:  []string{"profile:controller"},
		},
	}}}
	findings := AnalyzeGuards(cfg)
	if got := countRule(findings, RuleGuardMisuse); got != 1 {
		t.Fatalf("guard-misuse findings = %d, want 1: %+v", got, findings)
	}
}

func TestGuardMisuse_ProfileLocalAndControllerBothRequired(t *testing.T) {
	cfg := &pipelines.Config{Pipelines: []pipelines.Pipeline{{
		Name:       "deploy",
		Entrypoint: "Deploy",
		Guards:     pipelines.Guards{Require: []string{"profile:local", "profile:controller"}},
	}}}
	findings := AnalyzeGuards(cfg)
	if got := countRule(findings, RuleGuardMisuse); got != 1 {
		t.Fatalf("guard-misuse findings = %d, want 1: %+v", got, findings)
	}
}

func TestGuardMisuse_DuplicateToken(t *testing.T) {
	cfg := &pipelines.Config{Pipelines: []pipelines.Pipeline{{
		Name:       "deploy",
		Entrypoint: "Deploy",
		Guards:     pipelines.Guards{Require: []string{"git:branch=main", "git:branch=main"}},
	}}}
	findings := AnalyzeGuards(cfg)
	if got := countRule(findings, RuleGuardMisuse); got != 1 {
		t.Fatalf("guard-misuse findings = %d, want 1: %+v", got, findings)
	}
}

func TestGuardMisuse_CleanGuardsPass(t *testing.T) {
	cfg := &pipelines.Config{Pipelines: []pipelines.Pipeline{{
		Name:       "deploy",
		Entrypoint: "Deploy",
		Guards:     pipelines.Guards{Require: []string{"profile:controller"}, Reject: []string{"git:branch=main"}},
	}}}
	if findings := AnalyzeGuards(cfg); len(findings) != 0 {
		t.Fatalf("clean guards should produce no findings: %+v", findings)
	}
}

func TestRules_EveryRuleIsDocumented(t *testing.T) {
	want := map[string]bool{
		RulePlanIO: false, RulePlanRuntimeBranch: false, RuleRunnerLabel: false,
		RuleUnusedRef: false, RuleGuardMisuse: false,
	}
	for _, d := range Rules() {
		if _, ok := want[d.Name]; !ok {
			t.Fatalf("Rules() returned undocumented rule %q", d.Name)
		}
		if d.Forbids == "" || d.Why == "" {
			t.Fatalf("rule %q missing forbids/why", d.Name)
		}
		want[d.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("rule %q has no RuleDoc", name)
		}
	}
}

func TestAnalyze_SortsFindingsByLocation(t *testing.T) {
	src := fixtureHeader + `func (p *P) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	os.ReadFile("/a")
	os.Getenv("B")
	return nil
}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := Analyze(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("want 2 findings, got %+v", findings)
	}
	if findings[0].Line > findings[1].Line {
		t.Fatalf("findings not sorted by line: %+v", findings)
	}
}
