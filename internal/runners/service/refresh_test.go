package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

func refreshRegistration(ctx context.Context, h Host) (State, error) {
	return State{}, RefreshRegistration(ctx, h)
}

func TestRefreshRegistrationLeavesForeignAndOtherRegistrationsUntouched(t *testing.T) {
	for _, fixture := range []struct {
		name string
		host func(*testing.T, *fakeExec) Host
		path func(Host) string
	}{{"linux", linuxHost, UnitPath}, {"darwin", darwinHost, plistPath}} {
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
				err = RefreshRegistration(t.Context(), h)
				state, inspectErr := Inspect(t.Context(), h)
				if err == nil || inspectErr != nil || (foreign && !state.Foreign) || (!foreign && !state.Mismatch) {
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
	}{{"linux", linuxHost, UnitPath}, {"darwin", darwinHost, plistPath}} {
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
			for _, operation := range []func(context.Context, Host) (State, error){Install, refreshRegistration, Uninstall} {
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

func TestRegistrationConfigUsesTheFinalFlag(t *testing.T) {
	for _, fixture := range []struct {
		name string
		host func(*testing.T, *fakeExec) Host
		path func(Host) string
	}{{"linux", linuxHost, UnitPath}, {"darwin", darwinHost, plistPath}} {
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
			state, err := Inspect(t.Context(), h)
			if err != nil || !state.Mismatch || !strings.Contains(state.Detail, other) {
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
		body := []byte("# " + marker + "\n[Service]\nExecStart=" + unitfile.SystemdWord(h.Binary) + " " + args + "\n")
		if err := os.WriteFile(UnitPath(h), body, 0o600); err != nil {
			t.Fatal(err)
		}
		exec.calls = nil
		state, err := Inspect(t.Context(), h)
		if err != nil || !state.Mismatch || len(exec.calls) != 0 {
			t.Fatalf("consumed argument reinterpreted: %q %+v err=%v calls=%v", args, state, err, exec.calls)
		}
		after, err := os.ReadFile(UnitPath(h))
		if err != nil || !slices.Equal(body, after) {
			t.Fatalf("mismatched registration changed: %v", err)
		}
	}
}

func TestServiceCancellationAndUnsupportedPlatform(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, operation := range []func(context.Context, Host) (State, error){Install, Uninstall, Inspect, refreshRegistration} {
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
	if err := RefreshRegistration(ctx, h); !errors.Is(err, want) {
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
		body := []byte("# " + marker + "\n[Service]\nExecStart=" + command + "\n")
		if command != "" {
			body = append(body, valid...)
		}
		if err := os.WriteFile(UnitPath(h), body, 0o600); err != nil {
			t.Fatal(err)
		}
		exec.calls = nil
		for _, operation := range []func(context.Context, Host) (State, error){Inspect, refreshRegistration, Uninstall} {
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

func TestRefreshRegistrationPreservesRunningProcess(t *testing.T) {
	exec := &fakeExec{}
	host := linuxHost(t, exec)
	state, err := Install(t.Context(), host)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(state.Path)
	if err != nil {
		t.Fatal(err)
	}
	exec.calls = nil
	if err := RefreshRegistration(t.Context(), host); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(state.Path)
	if err != nil || !slices.Equal(before, after) {
		t.Fatal("registration changed during in-process replacement")
	}
	joined := strings.Join(exec.calls, " ")
	if !strings.Contains(joined, "daemon-reload") || strings.Contains(joined, "restart") || strings.Contains(joined, "stop") {
		t.Fatalf("running process lifecycle changed: %s", joined)
	}
}
