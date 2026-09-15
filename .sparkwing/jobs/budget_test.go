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

func TestBudgetFailsAnOverrunNamingTheSlowestStep(t *testing.T) {
	_, err := budgetVerdict("pre-push", 10*time.Second, 21*time.Second,
		[]stepTiming{{"gofmt", 200 * time.Millisecond}, {"lint-touched", 20 * time.Second}}, false, -1)
	if err == nil {
		t.Fatal("a tier at twice its budget passed")
	}
	if !strings.Contains(err.Error(), "lint-touched") || !strings.Contains(err.Error(), "20s") {
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
		w := sparkwing.NewWork()
		if _, err := work.Work(w); err != nil {
			t.Fatal(err)
		}
		verdict := w.StepByID(budgetStepID)
		if verdict == nil {
			t.Errorf("%s declares no %q step, so nothing fails when the tier outgrows its class", tier, budgetStepID)
			continue
		}
		var checks []string
		for _, s := range w.Steps() {
			if s.ID() != budgetStepID {
				checks = append(checks, s.ID())
			}
		}
		timed := verdict.DepIDs()
		slices.Sort(checks)
		slices.Sort(timed)
		if !slices.Equal(checks, timed) {
			t.Errorf("%s times %v but runs %v; a step outside the budget is a step that can grow unmeasured", tier, timed, checks)
		}
	}
}

func TestBudgetRecordsACompletedStep(t *testing.T) {
	b := newTierBudget("probe", 3*time.Second)
	w := sparkwing.NewWork()
	b.step(w, "cheap", func(context.Context) error { return nil })
	if _, err := sparkwing.RunWork(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if len(b.steps) != 1 || b.steps[0].id != "cheap" || b.steps[0].took < 0 {
		t.Fatalf("completed step timing = %+v, want one nonnegative duration for cheap", b.steps)
	}
}

func TestBudgetFailsTheTierWhenItsRecordedStepsOverrun(t *testing.T) {
	b := recordedOverrun()
	w := sparkwing.NewWork()
	b.verdict(w)

	_, err := sparkwing.RunWork(t.Context(), w)
	if err == nil {
		t.Fatal("a tier past its budget passed; nothing stops a class from regrowing")
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Errorf("the failure does not name the slowest step: %v", err)
	}
}

func TestBudgetReportsAfterAFailedStepWithoutOverridingIt(t *testing.T) {
	reported := false
	b := newTierBudget("probe", 3*time.Second).over(
		func(context.Context, string, func([]string) []string) ([]string, string, error) {
			reported = true
			return nil, "no Go files", nil
		})
	w := sparkwing.NewWork()
	w.ParallelFailures(sparkwing.FailFast)
	started := make(chan struct{})
	b.step(w, "red", func(context.Context) error {
		<-started
		return errors.New("the check found something")
	})
	b.step(w, "cancelled", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	b.verdict(w)

	_, err := sparkwing.RunWork(t.Context(), w)
	if err == nil {
		t.Fatal("a failed check passed")
	}
	if !strings.Contains(err.Error(), "the check found something") {
		t.Errorf("the budget replaced the check's own verdict: %v", err)
	}
	if !reported {
		t.Error("the budget verdict did not run after fail-fast cancelled a sibling step")
	}
}

// safety: the fileset the waiver reads is the tier's own scope, so a stub
// scope is what puts a test on either side of the threshold.
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
	b := recordedOverrun().over(filesetOf(count))
	w := sparkwing.NewWork()
	b.verdict(w)
	_, err := sparkwing.RunWork(t.Context(), w)
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

func TestBudgetEnforcesWhenTheFilesetCannotBeRead(t *testing.T) {
	b := recordedOverrun().over(
		func(context.Context, string, func([]string) []string) ([]string, string, error) {
			return nil, "", errors.New("no baseline")
		})
	w := sparkwing.NewWork()
	b.verdict(w)

	if _, err := sparkwing.RunWork(t.Context(), w); err == nil {
		t.Fatal("a tier that cannot size its change stopped keeping its promise")
	}
}

func TestReleaseCutRunsStateChecksAndTheFastBuildClass(t *testing.T) {
	want := []string{"budget", "build", "changelog-links", "lint", "sdk-pins", "test", "version-freshness"}
	if got := stepIDs(t, &releaseCutChecksJob{}); !slices.Equal(got, want) {
		t.Fatalf("release cut steps = %v, want %v", got, want)
	}
	w := sparkwing.NewWork()
	if _, err := (&releaseCutChecksJob{}).Work(w); err != nil {
		t.Fatal(err)
	}
	for _, s := range w.Steps() {
		if s.ID() == budgetStepID {
			continue
		}
		if deps := s.DepIDs(); len(deps) != 0 {
			t.Errorf("step %q waits on %v; the class is budgeted at its parallel cost, not the sum of its members", s.ID(), deps)
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
