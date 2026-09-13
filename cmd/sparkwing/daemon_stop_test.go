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
	deps := daemonStopDeps{
		stop: func(_ context.Context, opts wingdclient.Options) (wingdclient.StopResult, error) {
			asked = opts
			return wingdclient.StopResult{StoppedVersion: "v0.37.4", HoldersRemaining: 2, Stopped: true}, nil
		},
		inspect: func(context.Context, string) (daemonReport, error) {
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
	if report.PreviousVersion != "v0.37.4" || report.HoldersRemaining != 2 {
		t.Fatalf("report = %+v", report)
	}
	if asked.Version == "" {
		t.Error("stop was asked without the installed version")
	}
}

func TestDaemonStopOnAnAbsentDaemonSucceeds(t *testing.T) {
	deps := daemonStopDeps{
		stop: func(context.Context, wingdclient.Options) (wingdclient.StopResult, error) {
			return wingdclient.StopResult{}, wingdclient.ErrNoDaemon
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
		stop: func(context.Context, wingdclient.Options) (wingdclient.StopResult, error) {
			return wingdclient.StopResult{StoppedVersion: "v0.37.4"}, nil
		},
		inspect: func(context.Context, string) (daemonReport, error) {
			return daemonReport{Running: true}, nil
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
