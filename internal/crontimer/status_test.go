package crontimer

import (
	"errors"
	"strings"
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
	for _, test := range []struct {
		name, goos string
		failure    rule
	}{
		{"linux/unreachable", "linux", rule{prefix: "systemctl --user show --property=", err: errors.New("service manager unavailable")}},
		{"linux/other-source", "linux", rule{prefix: "systemctl --user show --property=", out: "FragmentPath=/other/timer\nActiveState=active\nUnitFileState=enabled\n"}},
		{"darwin/other-source", "darwin", rule{prefix: "launchctl print", out: "path = /other/timer.plist\n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec := &fakeExec{}
			h := linuxHost(t, exec)
			if test.goos == "darwin" {
				h = darwinHost(t, exec)
			}
			if _, err := Install(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			exec.rules = []rule{test.failure}
			state, err := Status(t.Context(), h)
			if err == nil || state.Enabled || !state.ActivationUnknown {
				t.Fatalf("status=%+v error=%v", state, err)
			}
		})
	}
}

func TestStatusJudgesTheRegistrationRatherThanTheObserversExecutable(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	installed, err := Install(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	exec.rules = []rule{{prefix: "systemctl --user show --property=", out: "FragmentPath=" + installed.Path + "\nActiveState=active\nUnitFileState=enabled\n"}}
	observer := h
	observer.Binary = fixtureAbsolutePath("/home/user/.sparkwing/engines/releases/v1.2.3/sparkwing-linux-amd64")
	state, err := Status(t.Context(), observer)
	if err != nil || !state.Installed || !state.Enabled || state.Stale || state.Binary != h.Binary {
		t.Fatalf("a controller running another executable judged a working timer: %+v %v", state, err)
	}
}

func TestActivateEnablesATimerWhoseInstallStoppedBeforeLinking(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	exec.fail("systemctl --user link", "Failed to link unit")
	if _, err := Install(t.Context(), h); err == nil {
		t.Fatal("install reported success although linking failed")
	}
	exec.rules, exec.calls = nil, nil
	state, err := Activate(t.Context(), h)
	if err != nil || !state.Enabled || state.WrittenAt.IsZero() {
		t.Fatalf("activate: %+v %v", state, err)
	}
	servicePath, timerPath, _ := linuxPaths(h)
	want := []string{
		"systemctl --user show -p FragmentPath --value " + h.serviceName(),
		"systemctl --user show -p FragmentPath --value " + h.timerName(),
		"systemctl --user link " + servicePath + " " + timerPath,
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + h.timerName(),
	}
	if strings.Join(exec.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(exec.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestActivateRefusesWithoutAnInstalledTimer(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	if _, err := Activate(t.Context(), h); err == nil || len(exec.calls) != 0 {
		t.Fatalf("activated nothing: %v %v", err, exec.calls)
	}
}

func TestStatusReportsAnUnloadedTimerAsNotEnabled(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	if _, err := Install(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	exec.rules = []rule{{prefix: "systemctl --user show --property=", out: "FragmentPath=\nActiveState=inactive\nUnitFileState=\n"}}
	state, err := Status(t.Context(), h)
	if err != nil || !state.Installed || state.Enabled || state.ActivationUnknown {
		t.Fatalf("an unlinked timer read as unknown or enabled: %+v %v", state, err)
	}
}
