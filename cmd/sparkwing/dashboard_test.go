package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

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
		{[]string{"start", "--addr", "[::1]:9000"}, []string{"start"}, nil},
		{[]string{"status"}, []string{"status"}, nil},
		{[]string{"stop"}, []string{"stop", "status"}, nil},
		{[]string{"install"}, []string{"install"}, nil},
		{[]string{"uninstall"}, []string{"status"}, []string{"uninstall"}},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			lifecycle := &dashboardLifecycleFake{status: wingd.LocalStatus{Running: true, Installed: true, PID: 123, Version: "v1.2.3", Activity: wingd.LocalActivity{Runs: 2, QueuedTriggers: 3, HeldLeases: 4, DashboardConnections: 5}}}
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
			if got.State != state || got.Version != "v1.2.3" || got.PID != 123 || !got.Installed || got.Activity != (dashboardActivity{Runs: 2, QueuedTriggers: 3, HeldLeases: 4, DashboardConnections: 5}) {
				t.Fatalf("lost controller status: %+v", got)
			}
			if tc.args[0] == "start" {
				want := services.options
				want.DashboardAddress = "[::1]:9000"
				if lifecycle.options != want {
					t.Fatalf("start options: %+v", lifecycle.options)
				}
			}
			if tc.args[0] == "install" && (lifecycle.options.Home != services.options.Home || lifecycle.options.Executable != services.options.Executable || lifecycle.options.Version != services.options.Version) {
				t.Fatalf("login options: %+v", lifecycle.options)
			}
		})
	}
}

func TestDashboardStatusPrintsTheServingURL(t *testing.T) {
	status := wingd.LocalStatus{Running: true, PID: 123, Version: "v1.2.3", DashboardURL: "http://127.0.0.1:4343"}
	for _, mode := range []string{"json", "pretty"} {
		lifecycle := &dashboardLifecycleFake{status: status}
		var output bytes.Buffer
		err := runDashboardCommandArgs(t.Context(), []string{"status", "-o", mode}, &output, "linux", func(context.Context) (dashboardServices, error) {
			return dashboardServices{lifecycle: lifecycle}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `"dashboard":"serving","url":"http://127.0.0.1:4343"`
		if mode == "pretty" {
			want = "Local controller: running\npid: 123\nversion: v1.2.3\ninstalled: false\ndashboard: serving at http://127.0.0.1:4343 (sign in with sparkwing dashboard open)\n"
		}
		if !strings.Contains(output.String(), want) {
			t.Fatalf("%s status lacks the dashboard URL: %s", mode, &output)
		}
	}
	lifecycle := &dashboardLifecycleFake{status: wingd.LocalStatus{Running: true, PID: 123, Version: "v1.2.3"}}
	var output bytes.Buffer
	if err := runDashboardCommandArgs(t.Context(), []string{"status", "-o", "json"}, &output, "linux", func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: lifecycle}, nil
	}); err != nil || strings.Contains(output.String(), `"url"`) || !strings.Contains(output.String(), `"state":"running","pid":123,"version":"v1.2.3","installed":false,"dashboard":"not_serving"`) {
		t.Fatalf("status without a serving dashboard named a URL: %s %v", &output, err)
	}
	output.Reset()
	if err := runDashboardCommandArgs(t.Context(), []string{"status", "-o", "pretty"}, &output, "linux", func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: lifecycle}, nil
	}); err != nil || !strings.Contains(output.String(), "Local controller: running\n") || !strings.Contains(output.String(), "dashboard: not serving (sparkwing dashboard open starts it") {
		t.Fatalf("pretty status does not separate the controller from a dashboard that is not serving: %s %v", &output, err)
	}
}

func TestDashboardDefaultStartUsesLocalController(t *testing.T) {
	f := &dashboardLifecycleFake{}
	var output bytes.Buffer
	err := runDashboardCommandArgs(t.Context(), []string{"start", "--read-only=false", "-o", "plain"}, &output, "windows", func(context.Context) (dashboardServices, error) { return dashboardServices{lifecycle: f}, nil })
	if err != nil || f.options.Standalone || f.options.DashboardAddress != "127.0.0.1:4343" {
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
		{"linux", []string{"start", "--stay"}},
		{"linux", []string{"start", "--stay=false"}},
		{"linux", []string{"status", "--output", "bogus"}},
		{"linux", []string{"logs", "--limit", "1"}},
		{"linux", []string{"logs", "--tail", "-1"}},
		{"linux", []string{"start", "--addr", "127.0.0.1:notaport"}},
		{"linux", []string{"start", "--addr", "127.0.0.1:65536"}},
		{"linux", []string{"start", "--addr", "0.0.0.0:4343"}},
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
				if !errors.As(err, &failure) || !strings.Contains(failure.Hint, "sparkwing dashboard start") {
					t.Fatalf("missing dashboard start guidance: %v", err)
				}
			}
		})
	}
}

func TestDashboardHelpDoesNotResolveRuntime(t *testing.T) {
	for _, args := range [][]string{nil, {"start", "--help"}, {"install", "--help"}, {"open", "--help"}} {
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

func TestDashboardOpenPrintsSocketMintedLink(t *testing.T) {
	login := wingd.DashboardLogin{URL: "http://127.0.0.1:4343/login?token=abc", ExpiresAt: time.Unix(1_800_000_060, 0).UTC()}
	for _, mode := range []string{"json", "plain", "pretty"} {
		t.Run(mode, func(t *testing.T) {
			minted := 0
			services := dashboardServices{lifecycle: &dashboardLifecycleFake{}, mintLogin: func(context.Context) (wingd.DashboardLogin, error) { minted++; return login, nil }}
			var output bytes.Buffer
			if err := runDashboardCommandArgs(t.Context(), []string{"open", "-o", mode}, &output, "linux", func(context.Context) (dashboardServices, error) { return services, nil }); err != nil {
				t.Fatal(err)
			}
			if minted != 1 || !strings.Contains(output.String(), login.URL) {
				t.Fatalf("minted=%d output=%q", minted, &output)
			}
			if mode == "json" {
				var got dashboardLoginResult
				if err := json.Unmarshal(output.Bytes(), &got); err != nil || got.Action != "open" || got.URL != login.URL || !got.ExpiresAt.Equal(login.ExpiresAt) {
					t.Fatalf("json receipt %+v: %v", got, err)
				}
			}
			if mode == "plain" && output.String() != login.URL+"\n" {
				t.Fatalf("plain output %q", &output)
			}
		})
	}
	failure := &apierr.Error{Code: apierr.Unavailable, Message: "the dashboard is not serving", Hint: "run sparkwing dashboard start"}
	t.Run("starts a dashboard that is not serving", func(t *testing.T) {
		lifecycle := &dashboardLifecycleFake{status: wingd.LocalStatus{Running: true, PID: 123, Version: "v1.2.3"}}
		minted := 0
		services := dashboardServices{lifecycle: lifecycle, options: wingd.LocalStartOptions{Home: "/private/home", Version: "v1.2.3"}, mintLogin: func(context.Context) (wingd.DashboardLogin, error) {
			minted++
			if len(lifecycle.calls) == 0 {
				return wingd.DashboardLogin{}, failure
			}
			return login, nil
		}}
		var output bytes.Buffer
		if err := runDashboardCommandArgs(t.Context(), []string{"open", "-o", "plain"}, &output, "linux", func(context.Context) (dashboardServices, error) { return services, nil }); err != nil {
			t.Fatal(err)
		}
		want := services.options
		want.DashboardAddress = defaultDashboardAddress
		if minted != 2 || !reflect.DeepEqual(lifecycle.calls, []string{"start"}) || lifecycle.options != want || output.String() != login.URL+"\n" {
			t.Fatalf("minted=%d calls=%v options=%+v output=%q", minted, lifecycle.calls, lifecycle.options, &output)
		}
	})
	t.Run("keeps a refused start", func(t *testing.T) {
		refused := errors.New("dashboard listen: address already in use")
		lifecycle := &dashboardLifecycleFake{err: refused}
		services := dashboardServices{lifecycle: lifecycle, mintLogin: func(context.Context) (wingd.DashboardLogin, error) { return wingd.DashboardLogin{}, failure }}
		var output bytes.Buffer
		err := runDashboardCommandArgs(t.Context(), []string{"open"}, &output, "linux", func(context.Context) (dashboardServices, error) { return services, nil })
		if !errors.Is(err, refused) || output.Len() != 0 {
			t.Fatalf("error=%v output=%q", err, &output)
		}
	})
	var output bytes.Buffer
	err := runDashboardCommandArgs(t.Context(), []string{"open"}, &output, "linux", func(context.Context) (dashboardServices, error) {
		return dashboardServices{mintLogin: func(context.Context) (wingd.DashboardLogin, error) { return wingd.DashboardLogin{}, failure }}, nil
	})
	if !errors.Is(err, failure) || output.Len() != 0 {
		t.Fatalf("error=%v output=%q", err, &output)
	}
}

func TestDashboardRemoteStartUsesAuthenticatedController(t *testing.T) {
	lifecycle := &dashboardLifecycleFake{}
	var output bytes.Buffer
	err := runDashboardCommandArgs(t.Context(), []string{"start", "--addr", "192.0.2.1:4343", "--allow-remote", "--read-only"}, &output, "linux", func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: lifecycle}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lifecycle.calls, []string{"start"}) || lifecycle.options.DashboardAddress != "192.0.2.1:4343" || !lifecycle.options.DashboardReadOnly {
		t.Fatalf("start options: %+v calls: %v", lifecycle.options, lifecycle.calls)
	}
}

func TestDashboardStopReportsAnOlderDashboardStillRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("older dashboards ran only where signal 0 probes a pid")
	}
	live := exec.Command("cat")
	stdin, err := live.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = live.Wait() })
	reaped := exec.Command("true")
	if err := reaped.Run(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pid   int
		alive bool
	}{{live.Process.Pid, true}, {reaped.Process.Pid, false}} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, olderDashboardPIDFile), []byte(strconv.Itoa(tc.pid)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		lifecycle := &dashboardLifecycleFake{status: wingd.LocalStatus{Running: true}}
		services := dashboardServices{lifecycle: lifecycle, options: wingd.LocalStartOptions{Home: home}}
		var output bytes.Buffer
		if err := runDashboardCommandArgs(t.Context(), []string{"stop", "-o", "json"}, &output, "linux", func(context.Context) (dashboardServices, error) { return services, nil }); err != nil {
			t.Fatal(err)
		}
		var got dashboardResult
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.State != "stopped" || tc.alive != strings.Contains(got.Warning, fmt.Sprintf("kill %d", tc.pid)) {
			t.Fatalf("pid %d alive=%v: %+v", tc.pid, tc.alive, got)
		}
	}
}
