package crontimer

import (
	"errors"
	"testing"
)

func TestStatusWithoutNativeTimerDoesNotContactServiceManager(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			exec := &fakeExec{}
			h := linuxHost(t, exec)
			if goos == "darwin" {
				h = darwinHost(t, exec)
			}
			state, err := Status(t.Context(), h)
			if err != nil || state.Installed || state.Enabled || len(exec.calls) != 0 {
				t.Fatalf("status=%+v calls=%v error=%v", state, exec.calls, err)
			}
		})
	}
}

func TestStatusReportsNativeActivation(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		for _, enabled := range []bool{false, true} {
			t.Run(goos+"/"+map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
				exec := &fakeExec{}
				h := linuxHost(t, exec)
				if goos == "darwin" {
					h = darwinHost(t, exec)
				}
				installed, err := Install(t.Context(), h)
				if err != nil {
					t.Fatal(err)
				}
				if goos == "linux" {
					activation := "ActiveState=inactive\nUnitFileState=disabled\n"
					if enabled {
						activation = "ActiveState=active\nUnitFileState=enabled\n"
					}
					exec.rules = []rule{{prefix: "systemctl --user show --property=", out: "FragmentPath=" + installed.Path + "\n" + activation}}
				} else if !enabled {
					exec.fail("launchctl print", "Could not find service")
				}
				state, err := Status(t.Context(), h)
				if err != nil || !state.Installed || state.Enabled != enabled {
					t.Fatalf("status=%+v error=%v", state, err)
				}
			})
		}
	}
}

func TestStatusRefusesUnknownOrDifferentNativeActivation(t *testing.T) {
	for _, failure := range []rule{
		{prefix: "systemctl --user show --property=", err: errors.New("service manager unavailable")},
		{prefix: "systemctl --user show --property=", out: "FragmentPath=/other/timer\nActiveState=active\nUnitFileState=enabled\n"},
	} {
		t.Run(failure.out, func(t *testing.T) {
			exec := &fakeExec{}
			h := linuxHost(t, exec)
			if _, err := Install(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			exec.rules = []rule{failure}
			state, err := Status(t.Context(), h)
			if err == nil || state.Enabled {
				t.Fatalf("status=%+v error=%v", state, err)
			}
		})
	}
}
