package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

var unreachableManager = wingd.UserManagerUnreachable("the local runner cannot start", "Failed to connect to bus: No such file or directory")

func TestLocalRunRefusesIntakeWhenNoLocalRunnerCanStart(t *testing.T) {
	for _, detached := range []bool{true, false} {
		t.Run(fmt.Sprint("detached=", detached), func(t *testing.T) {
			var calls []string
			deps := localRuntimeFor(t, localTestAward(t), detached, &calls)
			resolve := deps.resolve
			checks := 0
			deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
				runtime, err := resolve(ctx, options)
				runtime.ensureController = func(context.Context) error { checks++; return nil }
				runtime.runner = func(context.Context) (*apierr.Error, error) {
					if checks != 2 {
						t.Fatalf("runner checked before both controller readiness checks: %d", checks)
					}
					calls = append(calls, "runner")
					return unreachableManager, nil
				}
				return runtime, err
			}
			err := runLocalCommand(t.Context(), "ci", runFlags{detached: detached}, nil, io.Discard, deps)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Message != unreachableManager.Message || failure.Hint != unreachableManager.Hint {
				t.Fatalf("run without a local runner = %v", err)
			}
			if want := []string{"resolve", "prepare", "runner", "close"}; !slices.Equal(calls, want) {
				t.Fatalf("calls=%v, want %v", calls, want)
			}
		})
	}
}

func TestLocalRunnerUnavailableWaitsOutTheFirstCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []wingd.LocalStatus
		want   *apierr.Error
	}{
		{"unavailable", []wingd.LocalStatus{{Runner: wingd.LocalRunnerStarting}, {Runner: wingd.LocalRunnerStarting}, {Runner: wingd.LocalRunnerUnavailable, RunnerError: unreachableManager}}, unreachableManager},
		{"running", []wingd.LocalStatus{{Runner: wingd.LocalRunnerStarting}, {Runner: wingd.LocalRunnerRunning}}, nil},
		{"older controller", []wingd.LocalStatus{{}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reads := 0
				failure, err := localRunnerUnavailable(t.Context(), func(context.Context) (wingd.LocalStatus, error) {
					status := tc.states[reads]
					reads++
					return status, nil
				})
				if err != nil || failure != tc.want || reads != len(tc.states) {
					t.Fatalf("failure=%v err=%v reads=%d", failure, err, reads)
				}
			})
		})
	}
}

func TestDoctorReportsALocalRunnerThatCannotStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		local := &doctorLifecycleFake{statuses: []wingd.LocalStatus{
			{Running: true, Runner: wingd.LocalRunnerStarting},
			{Running: true, Runner: wingd.LocalRunnerUnavailable, RunnerError: unreachableManager},
		}}
		var out bytes.Buffer
		if err := runDoctorCommandArgs(t.Context(), []string{"-o", "json"}, &out, doctorTestDependencies(local)); err != nil {
			t.Fatal(err)
		}
		var got doctorReport
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := []doctorFinding{{Code: "local_runner_unavailable", Message: unreachableManager.Message, Hint: unreachableManager.Hint}}
		if !slices.Equal(got.Findings, want) || !got.ControllerChecked {
			t.Fatalf("doctor findings %+v", got)
		}
	})
}

func TestDoctorLeavesARunnerStillStartingUnreported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		local := &doctorLifecycleFake{statuses: []wingd.LocalStatus{{Running: true, Runner: wingd.LocalRunnerStarting}}}
		var out bytes.Buffer
		if err := runDoctorCommandArgs(t.Context(), []string{"-o", "json"}, &out, doctorTestDependencies(local)); err != nil {
			t.Fatal(err)
		}
		var got doctorReport
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Findings) != 0 || !got.ControllerChecked || got.ControllerError != "" {
			t.Fatalf("doctor on a starting runner %+v", got)
		}
	})
}

func TestDaemonStatusShowsStoppingControllerAndRunner(t *testing.T) {
	stopping := localStatusResult("controller", "status", wingd.LocalStatus{PID: 42, Version: "v1", Stopping: true, Runner: wingd.LocalRunnerRunning})
	if stopping.State != "stopping" || stopping.Runner != "" {
		t.Fatalf("stopping controller = %+v", stopping)
	}
	if stopped := localStatusResult("controller", "status", wingd.LocalStatus{}); stopped.State != "stopped" {
		t.Fatalf("absent controller = %+v", stopped)
	}
	running := localStatusResult("controller", "status", wingd.LocalStatus{Running: true, PID: 42, Version: "v1", Runner: wingd.LocalRunnerUnavailable, RunnerError: unreachableManager})
	if running.State != "running" || running.Runner != wingd.LocalRunnerUnavailable || running.RunnerError != unreachableManager.Message || running.RunnerHint != unreachableManager.Hint {
		t.Fatalf("runner failure = %+v", running)
	}
	var out bytes.Buffer
	if err := renderDashboard(&out, running, "pretty"); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"runner: unavailable: " + unreachableManager.Message, "  hint: " + unreachableManager.Hint} {
		if !strings.Contains(out.String(), line+"\n") {
			t.Fatalf("status lacks %q:\n%s", line, &out)
		}
	}
}
