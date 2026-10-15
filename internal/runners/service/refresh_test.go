package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
					if err := os.WriteFile(path, []byte("operator registration"), 0600); err != nil {
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
			return output, nil
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
				body = []byte(strings.Replace(string(body), " start --config "+systemdWord(h.ConfigPath), " start --config "+systemdWord(h.ConfigPath)+" --config="+systemdWord(other), 1))
			} else {
				body = []byte(strings.Replace(string(body), "  </array>", "    <string>--config="+xmlText(other)+"</string>\n  </array>", 1))
			}
			if err := os.WriteFile(fixture.path(h), body, 0600); err != nil {
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
		body := []byte("# " + Marker + "\n[Service]\nExecStart=" + systemdWord(h.Binary) + " " + args + "\n")
		if err := os.WriteFile(UnitPath(h), body, 0600); err != nil {
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
	if err := os.WriteFile(UnitPath(h), []byte("# "+Marker+"\n[Service]\nExecStart=\n"), 0600); err != nil {
		t.Fatal(err)
	}
	exec.calls = nil
	if _, err := Refresh(t.Context(), h); err == nil {
		t.Fatal("malformed unit was replaced")
	}
	if len(exec.calls) != 0 {
		t.Fatalf("malformed registration reached manager: %v", exec.calls)
	}
}
