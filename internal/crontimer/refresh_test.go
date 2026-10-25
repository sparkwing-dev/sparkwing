package crontimer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRefreshKeepsCapturedTimerSettings(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			e := &fakeExec{}
			h := linuxHost(t, e)
			h.GOOS = goos
			h.Version = "v1.2.3"
			h.PathEnv = "/captured/tools:/usr/bin"
			h.Env = map[string]string{"GOPROXY": "https://proxy.example"}
			installed, err := Install(t.Context(), h)
			if err != nil {
				t.Fatal(err)
			}
			original := read(t, installed.Path)
			e.calls = nil
			h.Binary = filepath.Join(h.Home, "updated % bin", "sparkwing")
			h.Version = "v1.2.4"
			h.PathEnv = "/ambient/tools"
			h.Env = map[string]string{"GOPROXY": "https://ambient.example"}
			h.LogPath = filepath.Join(h.Home, "ambient.log")
			state, err := Refresh(t.Context(), h)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"__tick", "--home", h.SparkwingHome}
			if !state.Installed || state.Binary != h.Binary || state.Version != h.Version || state.Home != h.SparkwingHome || !slices.Equal(state.Arguments, want) {
				t.Fatalf("refreshed timer = %+v", state)
			}
			path := state.Path
			if goos == "linux" {
				path = filepath.Join(UnitDir(h), h.serviceName())
				if got := read(t, installed.Path); got != original {
					t.Fatal("refresh changed the timer cadence or enabled state")
				}
				if !slices.Equal(e.calls, []string{"systemctl --user show -p FragmentPath --value " + h.serviceName(), "systemctl --user show -p FragmentPath --value " + h.timerName(), "systemctl --user daemon-reload"}) {
					t.Fatalf("refresh enabled or restarted a timer: %v", e.calls)
				}
			}
			body := read(t, path)
			if !strings.Contains(body, "captured/tools") || !strings.Contains(body, "proxy.example") || strings.Contains(body, "ambient") || strings.Contains(body, "SPARKWING_HOME") || strings.Contains(body, "crons tick") {
				t.Fatalf("refresh lost captured settings or restored removed plumbing:\n%s", body)
			}
		})
	}
}

func TestRefreshLeavesMissingForeignAndOtherHomeTimersAlone(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		for _, mode := range []string{"missing", "foreign", "other_home", "symlink"} {
			t.Run(goos+"/"+mode, func(t *testing.T) {
				e := &fakeExec{}
				h := linuxHost(t, e)
				h.GOOS, h.Version = goos, "v1.2.3"
				if mode == "missing" {
					state, err := Refresh(t.Context(), h)
					if err != nil || state.Installed || len(e.calls) != 0 {
						t.Fatalf("refresh installed an absent timer: %+v %v %v", state, err, e.calls)
					}
					return
				}
				state, err := Install(t.Context(), h)
				if err != nil {
					t.Fatal(err)
				}
				path := state.Path
				if goos == "linux" {
					path = filepath.Join(UnitDir(h), h.serviceName())
				}
				body := read(t, path)
				switch mode {
				case "foreign":
					body = "human-managed file\n"
				case "other_home":
					body = strings.ReplaceAll(body, h.SparkwingHome, filepath.Join(h.Home, "another-home"))
				case "symlink":
					if err := os.Rename(path, path+".target"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+".target", path); err != nil {
						t.Fatal(err)
					}
				}
				if mode != "symlink" {
					if err := os.WriteFile(path, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				h.Binary = filepath.Join(h.Home, "new", "sparkwing")
				for _, operation := range []func(context.Context, Host) (State, error){Install, Refresh, Uninstall} {
					e.calls = nil
					state, err := operation(t.Context(), h)
					if (err == nil && !state.Foreign) || len(e.calls) != 0 || read(t, path) != body {
						t.Fatalf("operation mutated an unowned timer: %v %v", err, e.calls)
					}
				}
			})
		}
	}
}

func TestTimerOperationsHonorCancellation(t *testing.T) {
	e := &fakeExec{}
	h := linuxHost(t, e)
	h.Version = "v1.2.3"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, operation := range []func(context.Context, Host) (State, error){Install, Refresh, Inspect, Uninstall} {
		if _, err := operation(ctx, h); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled operation = %v", err)
		}
	}
	if len(e.calls) != 0 {
		t.Fatalf("cancelled operation contacted the service manager: %v", e.calls)
	}
}
