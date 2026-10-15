package service

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

func FuzzRefreshPreservesInstalledSettings(f *testing.F) {
	f.Add(false, []byte("credentials $USER 100%"))
	f.Add(true, []byte("credentials $USER 100%"))
	f.Fuzz(func(t *testing.T, darwin bool, value []byte) {
		exec := &fakeExec{}
		makeHost := linuxHost
		if darwin {
			makeHost = darwinHost
		}
		h := makeHost(t, exec)
		state, err := Install(t.Context(), h)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(state.Path)
		if err != nil {
			t.Fatal(err)
		}
		setting := "$USER 100% " + hex.EncodeToString(value)
		args := []string{h.Binary, "start", "--config", filepath.Join(h.Home, "superseded.yaml"), "--config", h.ConfigPath, "--repos", "github.com/team/*", "--checkout", filepath.Join(h.Home, setting), "--output", "plain"}
		installed, err := unitfile.RewriteCommand(h.GOOS, string(body), args, "")
		if err != nil {
			t.Fatal(err)
		}
		policy := "Environment=" + unitfile.Assignment("CUSTOM_SETTING", setting) + "\nRestartSec=17\n"
		if darwin {
			policy = "<key>CustomSetting</key><string>" + unitfile.XMLText(setting) + "</string>"
			installed = strings.Replace(installed, "</dict>\n</plist>", policy+"</dict>\n</plist>", 1)
		} else {
			installed += policy
		}
		if err := os.WriteFile(state.Path, []byte(installed), 0o600); err != nil {
			t.Fatal(err)
		}
		h.Binary = filepath.Join(h.Home, "updated $USER 100%", "sparkwing-runner")
		state, err = Refresh(t.Context(), h)
		if err != nil {
			t.Fatal(err)
		}
		updated, err := os.ReadFile(state.Path)
		if err != nil {
			t.Fatal(err)
		}
		args[0] = h.Binary
		if !slices.Equal(unitfile.Arguments(h.GOOS, string(updated)), args) || !strings.Contains(string(updated), policy) || state.Binary != h.Binary || !slices.Equal(state.Arguments, args[1:]) {
			t.Fatalf("refresh discarded installed settings: state=%+v body=%s", state, updated)
		}
	})
}

func TestRefreshLeavesForeignAndOtherRegistrationsUntouched(t *testing.T) {
	for _, fixture := range []struct {
		name string
		host func(*testing.T, *fakeExec) Host
		path func(Host) string
	}{{"linux", linuxHost, UnitPath}, {"darwin", darwinHost, PlistPath}} {
		t.Run(fixture.name, func(t *testing.T) {
			for _, foreign := range []bool{false, true} {
				exec := &fakeExec{}
				h := fixture.host(t, exec)
				if _, err := Install(t.Context(), h); err != nil {
					t.Fatal(err)
				}
				path := fixture.path(h)
				if foreign {
					if err := os.WriteFile(path, []byte("operator registration"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					h.ConfigPath = filepath.Join(h.Home, "other.yaml")
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				exec.calls = nil
				state, err := Refresh(t.Context(), h)
				if err == nil || (foreign && !state.Foreign) || (!foreign && !state.Mismatch) {
					t.Fatalf("refused registration=%+v err=%v", state, err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !slices.Equal(before, after) || len(exec.calls) != 0 {
					t.Fatalf("changed refused registration: %s %v calls=%v", after, err, exec.calls)
				}
			}
		})
	}
}

func TestServiceOperationsRefuseAnotherLoadedFile(t *testing.T) {
	for _, fixture := range []struct {
		name string
		host func(*testing.T, *fakeExec) Host
		path func(Host) string
	}{{"linux", linuxHost, UnitPath}, {"darwin", darwinHost, PlistPath}} {
		t.Run(fixture.name, func(t *testing.T) {
			h := fixture.host(t, &fakeExec{})
			state, err := Install(t.Context(), h)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(state.Path)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []func(context.Context, Host) (State, error){Install, Refresh, Uninstall} {
				calls := 0
				h.Exec = func(context.Context, string, ...string) (string, error) {
					calls++
					if h.GOOS == "linux" {
						return "/other/sparkwing-runner.service\n", nil
					}
					return "path = /other/runner.plist\nstate = running\n", nil
				}
				if _, err := operation(t.Context(), h); err == nil || calls != 1 {
					t.Fatalf("changed another loaded file: %v, calls=%d", err, calls)
				}
				after, err := os.ReadFile(fixture.path(h))
				if err != nil || !slices.Equal(before, after) {
					t.Fatalf("refused operation changed its file: %v", err)
				}
			}
		})
	}
}

func TestRefreshPreservesStoppedLaunchdRegistration(t *testing.T) {
	exec := &fakeExec{}
	h := darwinHost(t, exec)
	if _, err := Install(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	exec.calls = nil
	exec.fail = map[string]string{"launchctl print": "Could not find specified service"}
	h.Binary = filepath.Join(h.Home, "updated-runner")
	state, err := Refresh(t.Context(), h)
	if err != nil || !state.Installed || state.Running {
		t.Fatalf("stopped refresh=%+v %v", state, err)
	}
	if !slices.Equal(exec.calls, []string{"launchctl print gui/501/com.sparkwing.runner"}) {
		t.Fatalf("started stopped runner: %v", exec.calls)
	}
	state, err = Inspect(t.Context(), h)
	if err != nil || state.Binary != h.Binary {
		t.Fatalf("updated registration=%+v %v", state, err)
	}
}

func TestRefreshPreservesLoadedButStoppedLaunchdRegistration(t *testing.T) {
	exec := &fakeExec{}
	h := darwinHost(t, exec)
	if _, err := Install(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"state = not running\n", "state = spawn scheduled\n", "unrecognized output"} {
		var calls []string
		h.Exec = func(_ context.Context, name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return "path = " + PlistPath(h) + "\n" + output, nil
		}
		state, err := Refresh(t.Context(), h)
		if state.Running || (output == "unrecognized output" && err == nil) {
			t.Fatalf("stopped registration=%+v err=%v", state, err)
		}
		if !slices.Equal(calls, []string{"launchctl print gui/501/com.sparkwing.runner"}) {
			t.Fatalf("started a stopped registration: %v", calls)
		}
	}
}

func TestRegistrationConfigUsesTheFinalFlag(t *testing.T) {
	for _, fixture := range []struct {
		name string
		host func(*testing.T, *fakeExec) Host
		path func(Host) string
	}{{"linux", linuxHost, UnitPath}, {"darwin", darwinHost, PlistPath}} {
		t.Run(fixture.name, func(t *testing.T) {
			exec := &fakeExec{}
			h := fixture.host(t, exec)
			if _, err := Install(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(fixture.path(h))
			if err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(h.Home, "other.yaml")
			if h.GOOS == "linux" {
				body = []byte(strings.Replace(string(body), " start --config "+unitfile.SystemdWord(h.ConfigPath), " start --config "+unitfile.SystemdWord(h.ConfigPath)+" --config="+unitfile.SystemdWord(other), 1))
			} else {
				body = []byte(strings.Replace(string(body), "  </array>", "    <string>--config="+unitfile.XMLText(other)+"</string>\n  </array>", 1))
			}
			if err := os.WriteFile(fixture.path(h), body, 0o600); err != nil {
				t.Fatal(err)
			}
			exec.calls = nil
			state, err := Refresh(t.Context(), h)
			if err == nil || !state.Mismatch || !strings.Contains(state.Detail, other) {
				t.Fatalf("conflicting config=%+v err=%v", state, err)
			}
			state, err = Uninstall(t.Context(), h)
			if err != nil || !state.Mismatch || len(exec.calls) != 0 {
				t.Fatalf("conflicting uninstall=%+v err=%v calls=%v", state, err, exec.calls)
			}
			after, err := os.ReadFile(fixture.path(h))
			if err != nil || !slices.Equal(body, after) {
				t.Fatalf("conflicting registration changed: %v", err)
			}
		})
	}
}

func TestRegistrationConfigConsumesValuesUsingTheRunnerGrammar(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	if _, err := Install(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		"start --config --config=" + h.ConfigPath,
		"start --checkout --config=" + h.ConfigPath,
		"start --repos --config=" + h.ConfigPath,
	} {
		body := []byte("# " + Marker + "\n[Service]\nExecStart=" + unitfile.SystemdWord(h.Binary) + " " + args + "\n")
		if err := os.WriteFile(UnitPath(h), body, 0o600); err != nil {
			t.Fatal(err)
		}
		exec.calls = nil
		state, err := Refresh(t.Context(), h)
		if err == nil || !state.Mismatch || len(exec.calls) != 0 {
			t.Fatalf("consumed argument reinterpreted: %q %+v err=%v calls=%v", args, state, err, exec.calls)
		}
		after, err := os.ReadFile(UnitPath(h))
		if err != nil || !slices.Equal(body, after) {
			t.Fatalf("mismatched registration changed: %v", err)
		}
	}
}

func TestRefreshReportsEveryServiceManagerFailure(t *testing.T) {
	for _, test := range []struct {
		name, failure string
		host          func(*testing.T, *fakeExec) Host
	}{
		{"reload", "systemctl --user daemon-reload", linuxHost},
		{"restart", "systemctl --user try-restart", linuxHost},
		{"inspect", "launchctl print", darwinHost},
		{"unload", "launchctl bootout", darwinHost},
		{"load", "launchctl bootstrap", darwinHost},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec := &fakeExec{}
			h := test.host(t, exec)
			if _, err := Install(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			exec.calls = nil
			exec.fail = map[string]string{test.failure: "service manager refused"}
			state, err := Refresh(t.Context(), h)
			if err == nil || !state.Installed || !strings.Contains(err.Error(), "service manager refused") {
				t.Fatalf("refresh failure=%+v %v", state, err)
			}
			if last := exec.calls[len(exec.calls)-1]; !strings.HasPrefix(last, test.failure) {
				t.Fatalf("continued after failure: %v", exec.calls)
			}
			if _, err := os.Stat(state.Path); err != nil {
				t.Fatalf("registration lost after failure: %v", err)
			}
		})
	}
}

func TestServiceCancellationAndUnsupportedPlatform(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, operation := range []func(context.Context, Host) (State, error){Install, Uninstall, Inspect, Refresh} {
		if _, err := operation(ctx, h); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
		if _, err := operation(t.Context(), Host{GOOS: "windows"}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported=%v", err)
		}
	}
	if len(exec.calls) != 0 {
		t.Fatalf("cancelled operation touched manager: %v", exec.calls)
	}
	if _, err := os.Stat(UnitPath(h)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled operation wrote registration: %v", err)
	}
	if _, err := Install(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	want := errors.New("marker")
	ctx = context.WithValue(t.Context(), struct{}{}, want)
	h.Exec = func(received context.Context, _ string, _ ...string) (string, error) {
		if received != ctx {
			t.Error("service manager did not receive caller context")
		}
		return "", want
	}
	if _, err := Refresh(ctx, h); !errors.Is(err, want) {
		t.Fatalf("manager failure lost: %v", err)
	}
}

func TestInspectRefusesMalformedManagedRegistration(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	if _, err := Install(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	valid := "ExecStart=" + unitfile.Command([]string{h.Binary, "start", "--config", h.ConfigPath}) + "\n"
	for _, command := range []string{"", ":", `"unfinished`, `trailing\`, `"bad\q"`} {
		body := []byte("# " + Marker + "\n[Service]\nExecStart=" + command + "\n")
		if command != "" {
			body = append(body, valid...)
		}
		if err := os.WriteFile(UnitPath(h), body, 0o600); err != nil {
			t.Fatal(err)
		}
		exec.calls = nil
		for _, operation := range []func(context.Context, Host) (State, error){Inspect, Refresh, Uninstall} {
			if _, err := operation(t.Context(), h); err == nil {
				t.Fatalf("accepted malformed registration %q", command)
			}
		}
		after, err := os.ReadFile(UnitPath(h))
		if err != nil || !slices.Equal(body, after) || len(exec.calls) != 0 {
			t.Fatalf("malformed registration changed: %v calls=%v", err, exec.calls)
		}
	}
}
