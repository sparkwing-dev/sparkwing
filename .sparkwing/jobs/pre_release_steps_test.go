package jobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPreReleasePlanAllowsStoreRace(t *testing.T) {
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&PreRelease{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-release"})
	})
	nodes := plan.Jobs
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want one", len(nodes))
	}
	if got, want := time.Duration(nodes[0].TimeoutMS)*time.Millisecond, 120*time.Minute; got != want {
		t.Fatalf("pre-release node timeout = %s, want %s for the sharded store race and later checks", got, want)
	}
}

func TestPreReleaseWorkKeepsEveryCheckInOrder(t *testing.T) {
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&PreRelease{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-release"})
	})
	nodes := plan.Jobs
	if len(nodes) != 1 || nodes[0].ID != "pre-release" {
		t.Fatalf("nodes = %v, want pre-release", nodes)
	}
	if got := time.Duration(nodes[0].TimeoutMS) * time.Millisecond; got != 120*time.Minute {
		t.Fatalf("pre-release node timeout = %s, want 2h", got)
	}
	work := nodes[0]

	want := []string{
		"no-replace", "no-go-work", "module-tidy", "sparkwing-pin",
		"version-freshness", "go-directives", "pre-v1-policy", "gofmt", "lint", "race",
		"race-store", "store-postgres", "chaos", "release-vulnerability", "shell-portability",
		"hosted-mutation-guard", "vulnerability-script", "schema-parity-script",
		"changelog-script",
		"installer-report", "service-installer", "release-installer",
		"install-to-green", "shellcheck", "terraform", "markdownlint",
		"actionlint", "doc-examples", "cli-reference", "config-reference",
		"sdk-reference", "api-reference", "api-snapshot",
	}
	steps := work.Steps
	if len(steps) != len(want) {
		t.Fatalf("steps = %d, want %d", len(steps), len(want))
	}
	for i, step := range steps {
		if step.ID != want[i] {
			t.Fatalf("step %d = %q, want %q", i, step.ID, want[i])
		}
		if !step.ContinueOnError {
			t.Fatalf("step %q stops later release checks after a failure", step.ID)
		}
		var wantDeps []string
		if i > 0 {
			wantDeps = []string{want[i-1]}
		}
		if !slices.Equal(step.Needs, wantDeps) {
			t.Fatalf("step %q dependencies = %v, want %v", step.ID, step.Needs, wantDeps)
		}
	}
}

func TestPreReleaseFailuresDoNotSuppressLaterChecks(t *testing.T) {
	var ran []string
	check := func(id string, cause error) preReleaseCheck {
		return preReleaseCheck{id: id, run: func(context.Context) error { ran = append(ran, id); return cause }}
	}
	host, plan, doc := prepareWork(t, testJob(func(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
		addPreReleaseChecks(work, []preReleaseCheck{
			check("first-failure", errors.New("first failed")),
			check("second-failure", errors.New("second failed")),
			check("later-success", nil),
		})
		return nil, nil
	}))
	for i, step := range doc.Steps {
		var wantDeps []string
		if i > 0 {
			wantDeps = []string{doc.Steps[i-1].ID}
		}
		if !step.ContinueOnError || !slices.Equal(step.Needs, wantDeps) {
			t.Fatalf("check declarations = %+v", step)
		}
		_, err := host.RunStep(t.Context(), plan, "job", step.ID)
		if i < 2 && (err == nil || !strings.Contains(err.Error(), strings.TrimSuffix(step.ID, "-failure")+" failed")) {
			t.Fatalf("%s failure = %v", step.ID, err)
		}
		if i == 2 && err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"first-failure", "second-failure", "later-success"}; !slices.Equal(ran, want) {
		t.Fatalf("check order = %v, want %v", ran, want)
	}
}
