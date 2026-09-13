package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDaemonStopIsDispatched(t *testing.T) {
	for _, cmd := range allCommands {
		if cmd.Path == cmdDaemonStop.Path {
			return
		}
	}
	t.Fatalf("%s is not in the command registry", cmdDaemonStop.Path)
}

func TestDaemonStopReportsWhatItStopped(t *testing.T) {
	asked := wingdclient.Options{}
	inspections := 0
	deps := daemonStopDeps{
		stop: func(_ context.Context, opts wingdclient.Options) error {
			asked = opts
			return nil
		},
		inspect: func(context.Context, string) (daemonReport, error) {
			inspections++
			if inspections == 1 {
				return daemonReport{Running: true, Healthy: true, BinaryVersion: "v0.37.4"}, nil
			}
			return daemonReport{Socket: "/tmp/stopped.sock"}, nil
		},
	}
	var runErr error
	out := captureStdout(t, func() { runErr = runDaemonStopWith([]string{"-o", "json"}, deps) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	var report daemonReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Running || !report.Stopped {
		t.Fatalf("report = %+v", report)
	}
	if report.PreviousVersion != "v0.37.4" {
		t.Fatalf("the report does not name the build it stopped: %+v", report)
	}
	if asked.Version == "" {
		t.Error("stop was asked without the installed version")
	}
}

func TestDaemonStopOnAnAbsentDaemonStopsNothing(t *testing.T) {
	stops := 0
	deps := daemonStopDeps{
		stop: func(context.Context, wingdclient.Options) error {
			stops++
			return nil
		},
		inspect: func(context.Context, string) (daemonReport, error) {
			return daemonReport{Socket: "/tmp/stopped.sock"}, nil
		},
	}
	var runErr error
	out := captureStdout(t, func() { runErr = runDaemonStopWith([]string{"-o", "json"}, deps) })
	if runErr != nil {
		t.Fatalf("an absent daemon must be a no-op: %v", runErr)
	}
	if stops != 0 {
		t.Errorf("an absent daemon was drained %d time(s)", stops)
	}
	var report daemonReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Running || report.Stopped {
		t.Fatalf("report = %+v", report)
	}
}

func TestDaemonStopRefusesToClaimADaemonStillAnswering(t *testing.T) {
	deps := daemonStopDeps{
		stop: func(context.Context, wingdclient.Options) error { return nil },
		inspect: func(context.Context, string) (daemonReport, error) {
			return daemonReport{Running: true, BinaryVersion: "v0.37.4"}, nil
		},
	}
	err := runDaemonStopWith([]string{"-o", "json"}, deps)
	if err == nil {
		t.Fatal("expected an error when the daemon still answers")
	}
	if !strings.Contains(err.Error(), "v0.37.4") {
		t.Fatalf("err = %v, want it to name the daemon", err)
	}
}
