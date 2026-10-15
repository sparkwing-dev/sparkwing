package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRenderComputeLimitsPlain(t *testing.T) {
	t.Parallel()
	var view controllerwire.OperatorLimits
	view.Limits = map[string]int64{"max_global_runners": 50}
	runners := int64(3)
	view.Usage.Runners = &runners
	view.Usage.ByPrincipal = map[string]int64{"agent:b": 1, "agent:a": 2}

	var buf bytes.Buffer
	if err := writeComputeLimitsPlain(&buf, view); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "max_global_runners\t50\n") {
		t.Errorf("plain output is missing the guard:\n%s", out)
	}
	if !strings.Contains(out, "runners\t3\n") {
		t.Errorf("plain output is missing the runner count:\n%s", out)
	}
	if strings.Index(out, "runners.agent:a") > strings.Index(out, "runners.agent:b") {
		t.Errorf("per-principal lines are not sorted:\n%s", out)
	}
}

func TestRenderComputeLimits(t *testing.T) {
	t.Parallel()
	var view controllerwire.OperatorLimits
	view.Limits = map[string]int64{
		"max_concurrent_runners": 4,
		"max_global_runners":     50,
		"max_jobs_per_run":       0,
	}
	runners, alarm := int64(4), true
	view.Usage.Runners = &runners
	view.Usage.ByPrincipal = map[string]int64{"agent:cloud": 4, "agent:other": 0}
	view.Usage.AlarmReached = &alarm

	var buf bytes.Buffer
	if err := renderComputeLimits(&buf, view); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"MAX_CONCURRENT_RUNNERS", "4", "MAX_GLOBAL_RUNNERS", "50",
		"MAX_JOBS_PER_RUN", "unlimited", "CLOUD RUNNERS", "agent:cloud", "ALARM",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compute limits output is missing %q:\n%s", want, out)
		}
	}
	for name := range view.Limits {
		if !strings.Contains(out, strings.ToUpper(name)) {
			t.Errorf("output omits the guard %q:\n%s", name, out)
		}
	}
}

func TestRenderComputeLimitsOmitsUnavailableFleetUsage(t *testing.T) {
	t.Parallel()
	view := controllerwire.OperatorLimits{Limits: map[string]int64{}}
	var pretty, plain bytes.Buffer
	if err := renderComputeLimits(&pretty, view); err != nil {
		t.Fatal(err)
	}
	if err := writeComputeLimitsPlain(&plain, view); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pretty.String(), "CLOUD RUNNERS") || strings.Contains(pretty.String(), "\nALARM") ||
		strings.Contains(plain.String(), "\nrunners\t") || strings.Contains(pretty.String(), "MAX_JOBS_PER_RUN") || strings.Contains(plain.String(), "max_jobs_per_run") {
		t.Fatalf("unavailable fleet usage printed as zero: pretty=%q plain=%q", pretty.String(), plain.String())
	}
}

func TestRenderComputeLimitsShowsTheRequestBudgets(t *testing.T) {
	t.Parallel()
	var view controllerwire.OperatorLimits
	view.Budgets.ClaimsPerRunnerMinute = 9600
	view.Budgets.HeartbeatsPerRunnerMinute = 1200
	view.Budgets.IdleClaimPollSeconds = 5
	view.Budgets.IdleClaimPollEnforced = true
	view.Budgets.MaxLogStreamsPerPrincipal = 50

	var pretty, plain bytes.Buffer
	if err := renderComputeLimits(&pretty, view); err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := writeComputeLimitsPlain(&plain, view); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	for _, want := range []string{"CLAIMS_PER_RUNNER_MINUTE", "9600", "5s, enforced", "MAX_DOWNLOADS_PER_PRINCIPAL", "unlimited"} {
		if !strings.Contains(pretty.String(), want) {
			t.Errorf("budgets are missing %q:\n%s", want, pretty.String())
		}
	}
	if !strings.Contains(plain.String(), "heartbeats_per_runner_minute\t1200\n") {
		t.Errorf("plain budgets are missing the heartbeat budget:\n%s", plain.String())
	}
}
