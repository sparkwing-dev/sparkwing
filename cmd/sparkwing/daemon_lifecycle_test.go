package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type daemonLifecycleFake struct {
	wingd.LocalLifecycle
	status  wingd.LocalStatus
	options wingd.LocalStartOptions
	calls   []string
	failAt  string
	err     error
	keepUp  bool
	cancel  context.CancelFunc
	started *wingd.LocalStatus
}

func (f *daemonLifecycleFake) Status(context.Context) (wingd.LocalStatus, error) {
	f.calls = append(f.calls, "status")
	if f.failAt == "status" {
		return wingd.LocalStatus{}, f.err
	}
	return f.status, nil
}

func (f *daemonLifecycleFake) Stop(context.Context) error {
	f.calls = append(f.calls, "stop")
	if f.failAt == "stop" {
		return f.err
	}
	if !f.keepUp {
		f.status.Running = false
	}
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *daemonLifecycleFake) EnsureStarted(_ context.Context, options wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	f.calls = append(f.calls, "start")
	f.options = options
	if f.failAt == "start" {
		return wingd.LocalStatus{}, f.err
	}
	if f.started != nil {
		return *f.started, nil
	}
	f.status.Running, f.status.Version, f.status.Stay = true, options.Version, options.Stay
	f.status.PID = 17
	return f.status, nil
}

func TestDaemonLifecycleReportsSharedStatus(t *testing.T) {
	fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Stay: true, Installed: true, PID: 42, Version: "v1.2.3-dev+abcdef01", Activity: wingd.LocalActivity{Runs: 2, QueuedTriggers: 3, HeldLeases: 4, DashboardConnections: 5}}}
	var out bytes.Buffer
	err := runDaemonLifecycleCommandArgs(t.Context(), []string{"status", "-o", "json"}, &out, func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: fake}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Kind, Service, Action, State, Version string
		PID                                   int
		Stay, Installed                       bool
		Activity                              map[string]any
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Kind != "service" || report.Service != "controller" || report.Action != "status" || report.State != "running" || report.Version != fake.status.Version || report.PID != 42 || !report.Stay || !report.Installed || report.Activity["runs"] != float64(2) || report.Activity["queued_triggers"] != float64(3) || report.Activity["held_leases"] != float64(4) || report.Activity["dashboard_connections"] != float64(5) || !reflect.DeepEqual(fake.calls, []string{"status"}) {
		t.Fatalf("status: %+v calls=%v", report, fake.calls)
	}
}

func TestDaemonLifecyclePreservesStoppedAndCurrentControllers(t *testing.T) {
	for _, test := range []struct {
		args    []string
		running bool
	}{
		{[]string{"restart"}, false},
		{[]string{"restart", "--force"}, false},
		{[]string{"stop"}, false},
		{[]string{"restart"}, true},
	} {
		fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: test.running, Version: "v2"}}
		var out bytes.Buffer
		err := runDaemonLifecycleCommandArgs(t.Context(), append(test.args, "-o", "json"), &out, func(context.Context) (dashboardServices, error) {
			return dashboardServices{lifecycle: fake, options: wingd.LocalStartOptions{Version: "v2"}}, nil
		})
		if err != nil || !reflect.DeepEqual(fake.calls, []string{"status"}) {
			t.Fatalf("%v running=%t: calls=%v err=%v", test.args, test.running, fake.calls, err)
		}
	}
}

func TestDaemonLifecycleRestartDrainsAndKeepsRuntimeOptions(t *testing.T) {
	for _, test := range []struct {
		force bool
		old   string
	}{{false, "v1"}, {true, "v2"}} {
		fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Stay: true, Installed: true, Version: test.old}}
		options := wingd.LocalStartOptions{Home: "/private/home", Executable: "/bin/sparkwing", Version: "v2", DashboardAddress: "127.0.0.1:9000"}
		var out bytes.Buffer
		args := []string{"restart", "-o", "json"}
		if test.force {
			args = append(args, "--force")
		}
		err := runDaemonLifecycleCommandArgs(t.Context(), args, &out, func(context.Context) (dashboardServices, error) {
			return dashboardServices{lifecycle: fake, options: options}, nil
		})
		options.Stay = true
		var result struct {
			Restarted       bool   `json:"restarted"`
			PreviousVersion string `json:"previous_version"`
			Version         string `json:"version"`
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fake.calls, []string{"status", "stop", "status", "start"}) || fake.options != options || !result.Restarted || result.PreviousVersion != test.old || result.Version != "v2" {
			t.Fatalf("restart: %+v options=%+v calls=%v", result, fake.options, fake.calls)
		}
	}
}

func TestDaemonLifecycleRefusesUndrainedController(t *testing.T) {
	for _, action := range []string{"stop", "restart"} {
		fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Version: "v1"}, keepUp: true}
		var out bytes.Buffer
		err := runDaemonLifecycleCommandArgs(t.Context(), []string{action, "-o", "json"}, &out, func(context.Context) (dashboardServices, error) {
			return dashboardServices{lifecycle: fake, options: wingd.LocalStartOptions{Version: "v2"}}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "v1") || out.Len() != 0 || !reflect.DeepEqual(fake.calls, []string{"status", "stop", "status"}) {
			t.Fatalf("%s: calls=%v err=%v output=%s", action, fake.calls, err, &out)
		}
	}
}

func TestDaemonLifecycleFailuresAndCancellationEmitNoSuccess(t *testing.T) {
	failure := errors.New("lifecycle failed")
	for _, failAt := range []string{"status", "stop", "start"} {
		fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Version: "v1"}, failAt: failAt, err: failure}
		var out bytes.Buffer
		err := runDaemonLifecycleCommandArgs(t.Context(), []string{"restart", "-o", "json"}, &out, func(context.Context) (dashboardServices, error) {
			return dashboardServices{lifecycle: fake, options: wingd.LocalStartOptions{Version: "v2"}}, nil
		})
		if !errors.Is(err, failure) || out.Len() != 0 || fake.calls[len(fake.calls)-1] != failAt {
			t.Fatalf("%s: calls=%v err=%v output=%s", failAt, fake.calls, err, &out)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Version: "v1"}, cancel: cancel}
	var out bytes.Buffer
	err := runDaemonLifecycleCommandArgs(ctx, []string{"restart", "-o", "json"}, &out, func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: fake, options: wingd.LocalStartOptions{Version: "v2"}}, nil
	})
	if !errors.Is(err, context.Canceled) || out.Len() != 0 || !reflect.DeepEqual(fake.calls, []string{"status", "stop"}) {
		t.Fatalf("cancellation: calls=%v err=%v output=%s", fake.calls, err, &out)
	}
}

func TestDaemonLifecyclePreflightAndOutputFailures(t *testing.T) {
	for _, args := range [][]string{{"status", "-o", "yaml"}, {"restart", "--unknown"}, {"stop", "extra"}, {"status", "--profile", "remote"}} {
		var out bytes.Buffer
		err := runDaemonLifecycleCommandArgs(t.Context(), args, &out, func(context.Context) (dashboardServices, error) {
			t.Fatal("invalid invocation resolved the controller")
			return dashboardServices{}, nil
		})
		if err == nil || exitCodeFor(err) != 2 || out.Len() != 0 {
			t.Fatalf("%v: %v output=%s", args, err, &out)
		}
	}
	var out bytes.Buffer
	if err := runDaemonLifecycleCommandArgs(t.Context(), []string{"status", "--help"}, &out, nil); err != nil || out.Len() == 0 {
		t.Fatalf("help: %v %s", err, &out)
	}
	err := runDaemonLifecycleCommandArgs(t.Context(), []string{"status", "-o", "json"}, doctorFailedWriter{}, func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: &daemonLifecycleFake{}}, nil
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure: %v", err)
	}
}

func TestDaemonLifecycleDoesNotClaimAnUnreadyRestart(t *testing.T) {
	for _, status := range []wingd.LocalStatus{{}, {Running: true, Version: "other"}} {
		fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Version: "v1"}, started: &status}
		var out bytes.Buffer
		err := runDaemonLifecycleCommandArgs(t.Context(), []string{"restart", "-o", "json"}, &out, func(context.Context) (dashboardServices, error) {
			return dashboardServices{lifecycle: fake, options: wingd.LocalStartOptions{Version: "v2"}}, nil
		})
		if err == nil || out.Len() != 0 {
			t.Fatalf("unready restart: %+v err=%v output=%s", status, err, &out)
		}
	}
}

func TestDaemonLifecycleMissingAndCancelledRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	if err := runDaemonLifecycleCommandArgs(ctx, []string{"stop"}, &out, nil); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancelled: %v %s", err, &out)
	}
	missing := func(context.Context) (dashboardServices, error) { return dashboardServices{}, nil }
	if err := runDaemonLifecycleCommandArgs(t.Context(), []string{"status", "-o", "json"}, &out, missing); err == nil || out.Len() != 0 {
		t.Fatalf("missing runtime: %v %s", err, &out)
	}
	want := errors.New("runtime construction failed")
	failed := func(context.Context) (dashboardServices, error) { return dashboardServices{}, want }
	if err := runDaemonLifecycleCommandArgs(t.Context(), []string{"status", "-o", "json"}, &out, failed); !errors.Is(err, want) || out.Len() != 0 {
		t.Fatalf("failed runtime: %v %s", err, &out)
	}
}

func TestShutdownWindowsNest(t *testing.T) {
	if wingd.FinalizeDrainWindow >= daemonRestartTimeout {
		t.Fatalf("shutdown windows: drain=%s restart=%s", wingd.FinalizeDrainWindow, daemonRestartTimeout)
	}
}
