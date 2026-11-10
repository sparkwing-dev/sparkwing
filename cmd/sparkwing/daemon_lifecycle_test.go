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

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
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
	f.status.Running, f.status.Version = true, options.Version
	f.status.PID = 17
	return f.status, nil
}

func TestDaemonLifecycleReportsSharedStatus(t *testing.T) {
	fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Installed: true, PID: 42, Version: "v1.2.3-dev+abcdef01", Activity: wingd.LocalActivity{Runs: 2, QueuedTriggers: 3, HeldLeases: 4, DashboardConnections: 5}}}
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
		Installed                             bool
		Activity                              map[string]any
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["stay"]; exists {
		t.Fatalf("status retained stay: %s", &out)
	}
	if _, exists := report.Activity["idle_since"]; exists {
		t.Fatalf("status retained idle_since: %s", &out)
	}
	if report.Kind != "service" || report.Service != "controller" || report.Action != "status" || report.State != "running" || report.Version != fake.status.Version || report.PID != 42 || !report.Installed || report.Activity["runs"] != float64(2) || report.Activity["queued_triggers"] != float64(3) || report.Activity["held_leases"] != float64(4) || report.Activity["dashboard_connections"] != float64(5) || !reflect.DeepEqual(fake.calls, []string{"status"}) {
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
		fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Installed: true, Version: test.old}}
		options := wingd.LocalStartOptions{Home: "/private/home", Executable: "/bin/sparkwing", Version: "v2"}
		var out bytes.Buffer
		args := []string{"restart", "-o", "json"}
		if test.force {
			args = append(args, "--force")
		}
		err := runDaemonLifecycleCommandArgs(t.Context(), args, &out, func(context.Context) (dashboardServices, error) {
			return dashboardServices{lifecycle: fake, options: options}, nil
		})
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

func TestDaemonStopRetiresTheRunnerAndRestartKeepsIt(t *testing.T) {
	for _, test := range []struct {
		args  []string
		calls []string
	}{
		{[]string{"stop"}, []string{"status", "retire", "stop", "status"}},
		{[]string{"restart", "--force"}, []string{"status", "stop", "status", "start"}},
	} {
		fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Version: "v2"}}
		services := dashboardServices{lifecycle: fake, options: wingd.LocalStartOptions{Home: "/private/home", Executable: "/bin/sparkwing", Version: "v2"}, retireRunner: func(context.Context) error {
			fake.calls = append(fake.calls, "retire")
			return nil
		}}
		var out bytes.Buffer
		err := runDaemonLifecycleCommandArgs(t.Context(), append(test.args, "-o", "json"), &out, func(context.Context) (dashboardServices, error) { return services, nil })
		if err != nil || !reflect.DeepEqual(fake.calls, test.calls) {
			t.Fatalf("%v: calls=%v err=%v", test.args, fake.calls, err)
		}
	}
	failure := errors.New("runner drain refused")
	fake := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, Version: "v2"}}
	services := dashboardServices{lifecycle: fake, retireRunner: func(context.Context) error { return failure }}
	var out bytes.Buffer
	err := runDaemonLifecycleCommandArgs(t.Context(), []string{"stop", "-o", "json"}, &out, func(context.Context) (dashboardServices, error) { return services, nil })
	if !errors.Is(err, failure) || out.Len() != 0 || !reflect.DeepEqual(fake.calls, []string{"status"}) {
		t.Fatalf("failed retirement: calls=%v err=%v", fake.calls, err)
	}
}

func TestDaemonInstallsAndRemovesTheControllerService(t *testing.T) {
	for _, test := range []struct {
		action       string
		calls, units []string
	}{
		{"install", []string{"install"}, nil},
		{"uninstall", []string{"status"}, []string{"uninstall"}},
	} {
		lifecycle := &dashboardLifecycleFake{status: wingd.LocalStatus{Running: true, Installed: test.action == "install", PID: 9, Version: "v2"}}
		units := &dashboardUnitsFake{}
		options := wingd.LocalStartOptions{Home: "/private/home", Executable: "/bin/sparkwing", Version: "v2"}
		var out bytes.Buffer
		err := runDaemonLifecycleCommandArgs(t.Context(), []string{test.action, "-o", "json"}, &out, func(context.Context) (dashboardServices, error) {
			return dashboardServices{lifecycle: lifecycle, units: units, options: options}, nil
		})
		if err != nil || !reflect.DeepEqual(lifecycle.calls, test.calls) || !reflect.DeepEqual(units.calls, test.units) {
			t.Fatalf("%s: lifecycle=%v units=%v err=%v", test.action, lifecycle.calls, units.calls, err)
		}
		if test.action == "install" && lifecycle.options != options {
			t.Fatalf("install options: %+v", lifecycle.options)
		}
		var got dashboardResult
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Service != "controller" || got.Action != test.action || got.State != "running" {
			t.Fatalf("%s receipt: %+v %v", test.action, got, err)
		}
	}
	failure := errors.New("unit manager refused")
	units := &dashboardUnitsFake{err: failure}
	var out bytes.Buffer
	err := runDaemonLifecycleCommandArgs(t.Context(), []string{"uninstall"}, &out, func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: &dashboardLifecycleFake{}, units: units}, nil
	})
	if !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("uninstall failure: %v %q", err, &out)
	}
}

func TestDaemonServiceRefusesWindowsBeforeResolvingServices(t *testing.T) {
	for _, action := range []string{"install", "uninstall"} {
		inv, err := clisurface.Parse("sparkwing", []string{"daemon", action})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = runDaemonLifecycleCommandOn(t.Context(), inv, &out, "windows", func(context.Context) (dashboardServices, error) {
			t.Fatal("resolved services on Windows")
			return dashboardServices{}, nil
		})
		var failure *apierr.Error
		if !errors.As(err, &failure) || !strings.Contains(failure.Hint, "sparkwing dashboard open") || out.Len() != 0 {
			t.Fatalf("%s on Windows: %v", action, err)
		}
	}
	if _, err := clisurface.Parse("sparkwing", []string{"daemon", "install", "--force"}); err == nil {
		t.Fatal("daemon install accepted the removed --force flag")
	}
}

func TestDaemonServiceRefusesInlineKeysBeforeResolvingServices(t *testing.T) {
	for _, name := range []string{"SPARKWING_SECRETS_KEY", "SPARKWING_SECRETS_PREVIOUS_KEY"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SPARKWING_SECRETS_KEY", "")
			t.Setenv("SPARKWING_SECRETS_PREVIOUS_KEY", "")
			secret := "private-key-value-must-not-be-persisted"
			t.Setenv(name, secret)
			inv, err := clisurface.Parse("sparkwing", []string{"daemon", "install"})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = runDaemonLifecycleCommandOn(t.Context(), inv, &out, "linux", func(context.Context) (dashboardServices, error) {
				t.Fatal("resolved services with an inline secrets key")
				return dashboardServices{}, nil
			})
			var failure *apierr.Error
			if !errors.As(err, &failure) || !strings.Contains(failure.Hint, "SPARKWING_SECRETS_KEY_FILE") || strings.Contains(err.Error(), secret) || out.Len() != 0 {
				t.Fatalf("inline key install refusal: %v", err)
			}
		})
	}
}
