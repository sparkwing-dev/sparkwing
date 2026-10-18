package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type dashboardLifecycleFake struct {
	wingd.LocalLifecycle
	calls   []string
	options wingd.LocalStartOptions
	status  wingd.LocalStatus
	err     error
	keepUp  bool
}

func (f *dashboardLifecycleFake) StartDashboard(_ context.Context, options wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	f.calls = append(f.calls, "start")
	f.options = options
	return f.status, f.err
}

func (f *dashboardLifecycleFake) InstallController(_ context.Context, options wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	f.calls = append(f.calls, "install")
	f.options = options
	return f.status, f.err
}

func (f *dashboardLifecycleFake) Stop(context.Context) error {
	f.calls = append(f.calls, "stop")
	if f.err == nil && !f.keepUp {
		f.status.Running = false
	}
	return f.err
}

func (f *dashboardLifecycleFake) Status(context.Context) (wingd.LocalStatus, error) {
	f.calls = append(f.calls, "status")
	return f.status, f.err
}

type dashboardUnitsFake struct {
	wingd.ManagedUnitManager
	calls []string
	err   error
}

func (f *dashboardUnitsFake) UninstallController(context.Context) error {
	f.calls = append(f.calls, "uninstall")
	return f.err
}

func TestDashboardUsesLocalControllerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		calls, units []string
	}{
		{[]string{"start", "--stay", "--addr", "[::1]:9000"}, []string{"start"}, nil},
		{[]string{"status"}, []string{"status"}, nil},
		{[]string{"stop"}, []string{"stop", "status"}, nil},
		{[]string{"install"}, []string{"install"}, nil},
		{[]string{"uninstall"}, []string{"status"}, []string{"uninstall"}},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			lifecycle := &dashboardLifecycleFake{status: wingd.LocalStatus{Running: true, Stay: true, Installed: true, PID: 123, Version: "v1.2.3", Activity: wingd.LocalActivity{Runs: 2, QueuedTriggers: 3, HeldLeases: 4, DashboardConnections: 5}}}
			units := &dashboardUnitsFake{}
			services := dashboardServices{lifecycle: lifecycle, units: units, options: wingd.LocalStartOptions{Home: "/private/home", Executable: "/bin/sparkwing", Version: "v2.0.0"}}
			var output bytes.Buffer
			err := runDashboardCommandArgs(t.Context(), append(tc.args, "-o", "json"), &output, "linux", func(context.Context) (dashboardServices, error) { return services, nil })
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(lifecycle.calls, tc.calls) || !reflect.DeepEqual(units.calls, tc.units) {
				t.Fatalf("lifecycle=%v units=%v", lifecycle.calls, units.calls)
			}
			var got dashboardResult
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			state := "running"
			if tc.args[0] == "stop" {
				state = "stopped"
			}
			if got.State != state || got.Version != "v1.2.3" || got.PID != 123 || !got.Stay || !got.Installed || got.Activity != (dashboardActivity{Runs: 2, QueuedTriggers: 3, HeldLeases: 4, DashboardConnections: 5}) {
				t.Fatalf("lost controller status: %+v", got)
			}
			if tc.args[0] == "start" {
				want := services.options
				want.Stay, want.DashboardAddress = true, "[::1]:9000"
				if lifecycle.options != want {
					t.Fatalf("start options: %+v", lifecycle.options)
				}
			}
			if tc.args[0] == "install" && (!lifecycle.options.Stay || lifecycle.options.Home != services.options.Home || lifecycle.options.Executable != services.options.Executable || lifecycle.options.Version != services.options.Version) {
				t.Fatalf("login options: %+v", lifecycle.options)
			}
		})
	}
}

func TestDashboardDefaultStartDoesNotPinController(t *testing.T) {
	f := &dashboardLifecycleFake{}
	var output bytes.Buffer
	err := runDashboardCommandArgs(t.Context(), []string{"start", "--stay=false", "--read-only=false", "-o", "plain"}, &output, "windows", func(context.Context) (dashboardServices, error) { return dashboardServices{lifecycle: f}, nil })
	if err != nil || f.options.Stay || f.options.Standalone || f.options.DashboardAddress != "127.0.0.1:4343" {
		t.Fatalf("options=%+v err=%v", f.options, err)
	}
	if output.String() != "stopped\n" {
		t.Fatalf("plain output: %q", &output)
	}
}

func TestDashboardRejectsBeforeResolvingServices(t *testing.T) {
	for _, tc := range []struct {
		goos string
		args []string
	}{
		{"linux", []string{"restart"}},
		{"linux", []string{"status", "--output", "bogus"}},
		{"linux", []string{"logs", "--limit", "1"}},
		{"linux", []string{"logs", "--tail", "-1"}},
		{"linux", []string{"start", "--addr", "127.0.0.1:notaport"}},
		{"linux", []string{"start", "--addr", "127.0.0.1:65536"}},
		{"linux", []string{"start", "--addr", "0.0.0.0:4343"}},
		{"linux", []string{"start", "--read-only"}},
		{"linux", []string{"start", "--allow-remote"}},
		{"linux", []string{"start", "--allow-origin", "http://example.com"}},
		{"linux", []string{"install", "--force"}},
		{"windows", []string{"install"}},
		{"windows", []string{"uninstall"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var output bytes.Buffer
			err := runDashboardCommandArgs(t.Context(), tc.args, &output, tc.goos, func(context.Context) (dashboardServices, error) {
				t.Fatal("resolved services for rejected invocation")
				return dashboardServices{}, nil
			})
			if err == nil || output.Len() != 0 {
				t.Fatalf("error=%v output=%s", err, &output)
			}
			if tc.goos == "windows" {
				var failure *apierr.Error
				if !errors.As(err, &failure) || !strings.Contains(failure.Hint, "--stay") {
					t.Fatalf("missing stay guidance: %v", err)
				}
			}
		})
	}
}

func TestDashboardHelpDoesNotResolveRuntime(t *testing.T) {
	for _, args := range [][]string{nil, {"start", "--help"}, {"install", "--help"}} {
		var output bytes.Buffer
		err := runDashboardCommandArgs(t.Context(), args, &output, "windows", func(context.Context) (dashboardServices, error) {
			t.Fatal("help resolved services")
			return dashboardServices{}, nil
		})
		if err != nil || output.Len() == 0 {
			t.Fatalf("help: %s %v", &output, err)
		}
	}
}

func TestDashboardPreservesFailuresWithoutSuccessReceipt(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Unavailable, Message: "admission drain failed"}
	for _, action := range []string{"start", "status", "stop", "install", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			lifecycle := &dashboardLifecycleFake{err: failure}
			units := &dashboardUnitsFake{err: failure}
			var output bytes.Buffer
			err := runDashboardCommandArgs(t.Context(), []string{action}, &output, "linux", func(context.Context) (dashboardServices, error) {
				return dashboardServices{lifecycle: lifecycle, units: units}, nil
			})
			if !errors.Is(err, failure) || output.Len() != 0 {
				t.Fatalf("error=%v output=%s", err, &output)
			}
			if action == "stop" && !reflect.DeepEqual(lifecycle.calls, []string{"stop"}) {
				t.Fatalf("status after failed stop: %v", lifecycle.calls)
			}
		})
	}
}

func TestDashboardMissingLifecycleFailsWithoutLegacyServiceFallback(t *testing.T) {
	var output bytes.Buffer
	err := runDashboardCommandArgs(t.Context(), []string{"start"}, &output, "linux", func(context.Context) (dashboardServices, error) {
		return dashboardServices{}, nil
	})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.Unavailable || output.Len() != 0 {
		t.Fatalf("error=%v output=%s", err, &output)
	}
}

func TestDashboardRefusesAnUndrainedStop(t *testing.T) {
	fake := &dashboardLifecycleFake{status: wingd.LocalStatus{Running: true, Version: "v1"}, keepUp: true}
	var out bytes.Buffer
	err := runDashboardCommandArgs(t.Context(), []string{"stop", "-o", "json"}, &out, "linux", func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: fake}, nil
	})
	if err == nil || out.Len() != 0 {
		t.Fatalf("undrained stop: %v %s", err, &out)
	}
}
