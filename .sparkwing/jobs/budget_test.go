package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestBudgetPassesATierInsideItsClass(t *testing.T) {
	line, overrun := budgetVerdict("pre-commit", 3*time.Second, 900*time.Millisecond,
		[]stepTiming{{"gofmt", 200 * time.Millisecond}, {"comments", 880 * time.Millisecond}}, false, -1)
	if overrun != "" {
		t.Fatalf("a tier inside its budget warned: %s", overrun)
	}
	if !strings.Contains(line, "comments") {
		t.Errorf("the verdict does not name the slowest step: %q", line)
	}
}

func TestBudgetJudgesTheSlowestCheckNotTheSpan(t *testing.T) {
	// safety: the shape pre-commit showed on a loaded host: checks queued for a
	// slot, so the span ran past the budget while no check came near it.
	line, overrun := budgetVerdict("pre-commit", preCommitBudget, 3808*time.Millisecond,
		[]stepTiming{{"gofmt", 300 * time.Millisecond}, {"test-sleeps", 473 * time.Millisecond}}, false, -1)
	if overrun != "" {
		t.Fatalf("time spent waiting for a slot counted against the checks: %s", overrun)
	}
	if !strings.Contains(line, "473ms") || !strings.Contains(line, "3.808s") {
		t.Errorf("the verdict does not report both the slowest check and the span: %q", line)
	}
}

func TestPrePushBudgetPassesAnElevenSecondCheck(t *testing.T) {
	line, overrun := budgetVerdict("pre-push", prePushBudget, 11*time.Second,
		[]stepTiming{{"build-touched", 10 * time.Second}, {"lint-touched", 11 * time.Second}}, false, -1)
	if overrun != "" {
		t.Fatalf("an eleven-second pre-push check warned: %s", overrun)
	}
	if !strings.Contains(line, "lint-touched") {
		t.Errorf("the verdict does not name the slowest step: %q", line)
	}
}

func TestPrePushBudgetWarnsPastOneMinuteNamingTheSlowestStep(t *testing.T) {
	_, overrun := budgetVerdict("pre-push", prePushBudget, time.Minute+time.Millisecond,
		[]stepTiming{{"build-touched", 59 * time.Second}, {"lint-touched", time.Minute + time.Millisecond}}, false, -1)
	if overrun == "" {
		t.Fatal("a pre-push check past one minute did not warn")
	}
	if !strings.Contains(overrun, "lint-touched") || !strings.Contains(overrun, "1m0.001s") {
		t.Errorf("the warning names neither the slowest step nor its cost: %s", overrun)
	}
}

func TestReleaseCutBudgetAcceptsTheMeasuredTestClass(t *testing.T) {
	took := 7*time.Minute + 19*time.Second + 746*time.Millisecond
	if _, overrun := budgetVerdict("release cut", releaseCutBudget+releaseLintLockAllowance, took,
		[]stepTiming{{"test", took}}, false, -1); overrun != "" {
		t.Fatalf("the measured release test class overran its budget: %s", overrun)
	}
}

func TestReleaseCutBudgetAcceptsContentionAndChecks(t *testing.T) {
	took := 30*time.Minute + 7*time.Minute + 19*time.Second
	if _, overrun := budgetVerdict("release cut", releaseCutBudget+releaseLintLockAllowance, took,
		[]stepTiming{{"lint", took}}, false, -1); overrun != "" {
		t.Fatalf("lint waiting for another tree exhausted the check allowance: %s", overrun)
	}
}

func TestReleaseCutBudgetWarnsPastFortyMinutesNamingTheSlowestStep(t *testing.T) {
	took := 40*time.Minute + time.Millisecond
	_, overrun := budgetVerdict("release cut", releaseCutBudget+releaseLintLockAllowance, took,
		[]stepTiming{{"test", took}}, false, -1)
	if overrun == "" {
		t.Fatal("a release cut check past forty minutes did not warn")
	}
	if !strings.Contains(overrun, "test") || !strings.Contains(overrun, "40m0.001s") {
		t.Errorf("the warning names neither the slowest step nor its cost: %s", overrun)
	}
}

func TestBudgetLeavesTheVerdictToTheCheckThatFailed(t *testing.T) {
	_, overrun := budgetVerdict("pre-push", 10*time.Second, 21*time.Second,
		[]stepTiming{{"gofmt", 20 * time.Second}}, true, -1)
	if overrun != "" {
		t.Fatalf("the budget judged a tier whose own check failed, so the push reports the wrong cause: %s", overrun)
	}
}

func TestBudgetJudgesNothingWhenNoStepRan(t *testing.T) {
	line, overrun := budgetVerdict("pre-commit", 3*time.Second, time.Hour, nil, false, -1)
	if line != "" || overrun != "" {
		t.Fatalf("budgetVerdict(no steps) = %q, %q; want silence", line, overrun)
	}
}

func TestBudgetTimesEveryStepTheTierDeclares(t *testing.T) {
	for tier, work := range map[string]interface {
		Work(*sparkwing.Work) (*sparkwing.WorkStep, error)
	}{
		"pre-commit":  &PreCommit{},
		"pre-push":    &PrePush{},
		"release cut": &releaseCutChecksJob{},
	} {
		w := inspectWork(t, work)
		verdict := findStep(w, budgetStepID)
		if verdict == nil {
			t.Errorf("%s declares no %q step, so nothing warns when the tier outgrows its class", tier, budgetStepID)
			continue
		}
		var checks []string
		for _, s := range w.Steps {
			if s.ID != budgetStepID {
				checks = append(checks, s.ID)
			}
		}
		timed := verdict.Needs
		slices.Sort(checks)
		slices.Sort(timed)
		if !slices.Equal(checks, timed) {
			t.Errorf("%s times %v but runs %v; a step outside the budget is a step that can grow unmeasured", tier, timed, checks)
		}
	}
}

func TestBudgetRecordsACompletedStep(t *testing.T) {
	b := newTierBudget("probe", 3*time.Second)
	host, plan, _ := prepareWork(t, testJob(func(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
		b.step(w, "cheap", func(context.Context) error { return nil })
		return nil, nil
	}))
	if _, err := host.RunStep(t.Context(), plan, "job", "cheap"); err != nil {
		t.Fatal(err)
	}
	if len(b.steps) != 1 || b.steps[0].id != "cheap" || b.steps[0].took < 0 {
		t.Fatalf("completed step timing = %+v", b.steps)
	}
}

func TestBudgetPassesAGreenTierThatOverruns(t *testing.T) {
	if err := runBudgetVerdict(t, recordedOverrun()); err != nil {
		t.Fatalf("a tier whose checks all passed failed on time alone: %v", err)
	}
}

func TestBudgetReportsAfterAFailedStepWithoutOverridingIt(t *testing.T) {
	reported := false
	b := newTierBudget("probe", 3*time.Second).over(func(context.Context, string, func([]string) []string) ([]string, string, error) {
		reported = true
		return nil, "no Go files", nil
	})
	host, plan, doc := prepareWork(t, testJob(func(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
		w.FailurePolicy(sparkwing.FailFast)
		b.step(w, "red", func(context.Context) error { return errors.New("the check found something") })
		b.verdict(w)
		return nil, nil
	}))
	verdict := findStep(doc, budgetStepID)
	if verdict == nil || !verdict.Finally || !slices.Equal(verdict.Needs, []string{"red"}) {
		t.Fatalf("budget declaration = %+v", verdict)
	}
	if _, err := host.RunStep(t.Context(), plan, "job", "red"); err == nil || !strings.Contains(err.Error(), "the check found something") {
		t.Fatalf("check verdict = %v", err)
	}
	if _, err := host.RunStep(t.Context(), plan, "job", budgetStepID); err != nil {
		t.Fatalf("budget replaced check failure: %v", err)
	}
	if !reported {
		t.Fatal("budget did not report after failed check")
	}
}

func filesetOf(count int) scopeFunc {
	return func(context.Context, string, func([]string) []string) ([]string, string, error) {
		files := make([]string, count)
		for i := range files {
			files[i] = fmt.Sprintf("pkg/file%d.go", i)
		}
		return files, fmt.Sprintf("%d Go file(s)", count), nil
	}
}

func runBudgetVerdict(t *testing.T, b *tierBudget) error {
	t.Helper()
	host, plan, _ := prepareWork(t, testJob(func(w *sparkwing.Work) (*sparkwing.WorkStep, error) { b.verdict(w); return nil, nil }))
	_, err := host.RunStep(t.Context(), plan, "job", budgetStepID)
	return err
}

func recordedOverrun() *tierBudget {
	b := newTierBudget("probe", 3*time.Second)
	start := time.Unix(0, 0)
	b.record("slow", start, start.Add(4*time.Second), nil)
	return b
}

func TestBudgetSaysWhenATierJudgedNoGoFile(t *testing.T) {
	b := newTierBudget("probe", 3*time.Second).over(filesetOf(0))
	count, scope := b.changedGoFiles(t.Context())
	if count != 0 {
		t.Fatalf("changedGoFiles = %d, want 0", count)
	}
	notice := emptyScopeNotice("probe", scope)
	if !strings.Contains(notice, scope) || !strings.Contains(notice, "covers nothing") {
		t.Errorf("an empty-scope verdict does not name what it covered: %q", notice)
	}
}

func TestReleaseCutRunsStateChecksAndTheFastBuildClass(t *testing.T) {
	want := []string{"budget", "build", "changelog-links", "docs-sidebar", "frontend-deps", "go-directives", "lint", "sdk-pins", "sdk-typescript", "test", "version-freshness"}
	if got := stepIDs(t, &releaseCutChecksJob{}); !slices.Equal(got, want) {
		t.Fatalf("release cut steps = %v, want %v", got, want)
	}
	w := inspectWork(t, &releaseCutChecksJob{})
	for _, s := range w.Steps {
		if s.ID == budgetStepID {
			continue
		}
		wantDeps := map[string][]string{
			"lint":              {"build"},
			"frontend-deps":     {"lint"},
			"sdk-typescript":    {"frontend-deps"},
			"test":              {"sdk-typescript"},
			"version-freshness": {"test"},
		}[s.ID]
		if slices.Contains([]string{"go-directives", "sdk-pins", "changelog-links", "docs-sidebar"}, s.ID) {
			wantDeps = []string{"version-freshness"}
		}
		if deps := s.Needs; !slices.Equal(deps, wantDeps) {
			t.Errorf("step %q waits on %v; want sequential Go checks followed by policy guards", s.ID, deps)
		}
	}
}

func TestTheFastLinterSubsetIsEnabledInTheFullSet(t *testing.T) {
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path")
	}
	data, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Linters struct {
			Enable []string `yaml:"enable"`
		} `yaml:"linters"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range prePushLinters {
		if !slices.Contains(cfg.Linters.Enable, name) {
			t.Errorf("the push tier runs %q, which .golangci.yml does not enable: the fast subset judges the push against a rule the release cut forgives", name)
		}
	}
	if len(prePushLinters) >= len(cfg.Linters.Enable) {
		t.Errorf("the fast subset names %d of the full set's %d linters, so it is not a subset", len(prePushLinters), len(cfg.Linters.Enable))
	}
}
