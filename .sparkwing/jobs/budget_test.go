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
	line, err := budgetVerdict("pre-commit", 3*time.Second, 900*time.Millisecond,
		[]stepTiming{{"gofmt", 200 * time.Millisecond}, {"comments", 880 * time.Millisecond}}, false, -1)
	if err != nil {
		t.Fatalf("a tier inside its budget failed: %v", err)
	}
	if !strings.Contains(line, "comments") {
		t.Errorf("the verdict does not name the slowest step: %q", line)
	}
}

func TestPrePushBudgetPassesAnElevenSecondSpan(t *testing.T) {
	line, err := budgetVerdict("pre-push", prePushBudget, 11*time.Second,
		[]stepTiming{{"build-touched", 10 * time.Second}, {"lint-touched", 11 * time.Second}}, false, -1)
	if err != nil {
		t.Fatalf("an eleven-second pre-push span failed its hard budget: %v", err)
	}
	if !strings.Contains(line, "lint-touched") {
		t.Errorf("the verdict does not name the slowest step: %q", line)
	}
}

func TestPrePushBudgetFailsPastOneMinuteNamingTheSlowestStep(t *testing.T) {
	_, err := budgetVerdict("pre-push", prePushBudget, time.Minute+time.Millisecond,
		[]stepTiming{{"build-touched", 59 * time.Second}, {"lint-touched", time.Minute + time.Millisecond}}, false, -1)
	if err == nil {
		t.Fatal("a pre-push span past one minute passed")
	}
	if !strings.Contains(err.Error(), "lint-touched") || !strings.Contains(err.Error(), "1m0.001s") {
		t.Errorf("the failure names neither the slowest step nor its cost: %v", err)
	}
}

func TestReleaseCutBudgetAcceptsTheMeasuredTestClass(t *testing.T) {
	span := 7*time.Minute + 19*time.Second + 746*time.Millisecond
	_, err := budgetVerdict("release cut", releaseCutBudget+releaseLintLockAllowance, span,
		[]stepTiming{{"test", span}}, false, -1)
	if err != nil {
		t.Fatalf("the measured release test class failed its budget: %v", err)
	}
}

func TestReleaseCutBudgetAcceptsContentionAndChecks(t *testing.T) {
	span := 30*time.Minute + 7*time.Minute + 19*time.Second
	_, err := budgetVerdict("release cut", releaseCutBudget+releaseLintLockAllowance, span,
		[]stepTiming{{"lint", span}}, false, -1)
	if err != nil {
		t.Fatalf("lint waiting for another tree exhausted the check allowance: %v", err)
	}
}

func TestReleaseCutBudgetFailsPastFortyMinutesNamingTheSlowestStep(t *testing.T) {
	span := 40*time.Minute + time.Millisecond
	_, err := budgetVerdict("release cut", releaseCutBudget+releaseLintLockAllowance, span,
		[]stepTiming{{"test", span}}, false, -1)
	if err == nil {
		t.Fatal("a release cut past forty minutes passed")
	}
	if !strings.Contains(err.Error(), "test") || !strings.Contains(err.Error(), "40m0.001s") {
		t.Errorf("the failure names neither the slowest step nor its cost: %v", err)
	}
}

func TestBudgetLeavesTheVerdictToTheCheckThatFailed(t *testing.T) {
	_, err := budgetVerdict("pre-push", 10*time.Second, 21*time.Second,
		[]stepTiming{{"gofmt", 20 * time.Second}}, true, -1)
	if err != nil {
		t.Fatalf("the budget judged a tier whose own check failed, so the push reports the wrong cause: %v", err)
	}
}

func TestBudgetJudgesNothingWhenNoStepRan(t *testing.T) {
	line, err := budgetVerdict("pre-commit", 3*time.Second, time.Hour, nil, false, -1)
	if err != nil || line != "" {
		t.Fatalf("budgetVerdict(no steps) = %q, %v; want silence", line, err)
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
			t.Errorf("%s declares no %q step, so nothing fails when the tier outgrows its class", tier, budgetStepID)
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

func TestBudgetFailsTheTierWhenItsRecordedStepsOverrun(t *testing.T) {
	err := runBudgetVerdict(t, recordedOverrun())
	if err == nil || !strings.Contains(err.Error(), "slow") {
		t.Fatalf("overrun = %v, want slow step failure", err)
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

func overrunWithFileset(t *testing.T, count int) error {
	t.Helper()
	return runBudgetVerdict(t, recordedOverrun().over(filesetOf(count)))
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

func TestBudgetEnforcesUpToTheFilesetATierIsBudgetedFor(t *testing.T) {
	for _, count := range []int{24, 25} {
		if err := overrunWithFileset(t, count); err == nil {
			t.Fatalf("a tier past its budget over %d Go files passed; the budget stops meaning anything", count)
		}
	}
}

func TestBudgetIsWaivedForAFilesetWiderThanATierIsBudgetedFor(t *testing.T) {
	for _, count := range []int{26, 27} {
		if err := overrunWithFileset(t, count); err != nil {
			t.Fatalf("a %d-file change failed the budget; these steps cost per file, so a wide change is not a tier that grew: %v",
				count, err)
		}
	}
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

func TestBudgetEnforcesWhenTheFilesetCannotBeRead(t *testing.T) {
	b := recordedOverrun().over(
		func(context.Context, string, func([]string) []string) ([]string, string, error) {
			return nil, "", errors.New("no baseline")
		})
	if err := runBudgetVerdict(t, b); err == nil {
		t.Fatal("a tier that cannot size its change stopped keeping its promise")
	}
}

func TestReleaseCutRunsStateChecksAndTheFastBuildClass(t *testing.T) {
	want := []string{"budget", "build", "changelog-links", "go-directives", "lint", "sdk-pins", "test", "version-freshness"}
	if got := stepIDs(t, &releaseCutChecksJob{}); !slices.Equal(got, want) {
		t.Fatalf("release cut steps = %v, want %v", got, want)
	}
	w := inspectWork(t, &releaseCutChecksJob{})
	for _, s := range w.Steps {
		if s.ID == budgetStepID {
			continue
		}
		if deps := s.Needs; len(deps) != 0 {
			t.Errorf("step %q waits on %v; the class is budgeted at its parallel cost, not the sum of its members", s.ID, deps)
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
