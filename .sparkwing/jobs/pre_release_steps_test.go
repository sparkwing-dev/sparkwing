package jobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPreReleaseWorkKeepsEveryCheckInOrder(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (&PreRelease{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-release"}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	nodes := plan.Nodes()
	if len(nodes) != 1 || nodes[0].ID() != "pre-release" {
		t.Fatalf("nodes = %v, want pre-release", nodes)
	}
	work := nodes[0].Work()
	if work == nil {
		t.Fatal("pre-release has no native Work steps")
	}

	want := []string{
		"no-replace", "no-go-work", "module-tidy", "sparkwing-pin",
		"version-freshness", "pre-v1-policy", "gofmt", "lint", "race",
		"store-postgres", "chaos", "release-vulnerability", "shell-portability",
		"hosted-mutation-guard", "vulnerability-script", "changelog-script",
		"installer-report", "service-installer", "release-installer",
		"install-to-green", "shellcheck", "terraform", "markdownlint",
		"actionlint", "doc-examples", "cli-reference", "config-reference",
		"sdk-reference", "api-reference", "openapi", "api-snapshot",
	}
	steps := work.Steps()
	if len(steps) != len(want) {
		t.Fatalf("steps = %d, want %d", len(steps), len(want))
	}
	for i, step := range steps {
		if step.ID() != want[i] {
			t.Fatalf("step %d = %q, want %q", i, step.ID(), want[i])
		}
		if !step.IsContinueOnError() {
			t.Fatalf("step %q stops later release checks after a failure", step.ID())
		}
		var wantDeps []string
		if i > 0 {
			wantDeps = []string{want[i-1]}
		}
		if !slices.Equal(step.DepIDs(), wantDeps) {
			t.Fatalf("step %q dependencies = %v, want %v", step.ID(), step.DepIDs(), wantDeps)
		}
	}
}

func TestPreReleaseFailuresDoNotSuppressLaterChecks(t *testing.T) {
	var ran []string
	check := func(id string, err error) preReleaseCheck {
		return preReleaseCheck{id: id, run: func(context.Context) error {
			ran = append(ran, id)
			return err
		}}
	}
	work := sparkwing.NewWork()
	addPreReleaseChecks(work, []preReleaseCheck{
		check("first-failure", errors.New("first failed")),
		check("second-failure", errors.New("second failed")),
		check("later-success", nil),
	})

	_, err := sparkwing.RunWork(context.Background(), work)
	if err == nil || !strings.Contains(err.Error(), "first failed") {
		t.Fatalf("RunWork error = %v, want failed parent naming first failure", err)
	}
	if want := []string{"first-failure", "second-failure", "later-success"}; !slices.Equal(ran, want) {
		t.Fatalf("checks run = %v, want %v", ran, want)
	}
}
