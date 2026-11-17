package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type updateControllerUnits struct {
	wingd.ManagedUnitManager
	installed bool
	err       error
	refreshed []string
}

func (u *updateControllerUnits) Refresh(_ context.Context, executable, version string) error {
	u.refreshed = []string{executable, version}
	return u.err
}

func (u *updateControllerUnits) ControllerInstalled(context.Context) (bool, error) {
	return u.installed, nil
}

func TestUpdateRefreshesRunningPrivateController(t *testing.T) {
	home := t.TempDir()
	executable := filepath.Join(home, "new-sparkwing")
	units := &updateControllerUnits{}
	lifecycle := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, PID: 42, Version: "v1.2.2"}}
	manager := updateLocalUnits{ManagedUnitManager: units, services: dashboardServices{
		lifecycle: lifecycle, options: wingd.LocalStartOptions{Home: home, Executable: filepath.Join(home, "old-sparkwing"), Version: "v1.2.2"},
		retireRunner: func(context.Context) error { t.Fatal("update retired held runner jobs"); return nil },
	}}
	if err := manager.Refresh(t.Context(), executable, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lifecycle.calls, []string{"status", "status", "stop", "status", "start"}) || lifecycle.status.Version != "v1.2.3" {
		t.Fatalf("old controller still serves work: status=%+v calls=%v", lifecycle.status, lifecycle.calls)
	}
	want := wingd.LocalStartOptions{Home: home, Executable: executable, Version: "v1.2.3"}
	if lifecycle.options != want || !reflect.DeepEqual(units.refreshed, []string{executable, "v1.2.3"}) {
		t.Fatalf("replacement options=%+v refresh=%v", lifecycle.options, units.refreshed)
	}
}

func TestUpdatePreservesManagedStoppedAndCurrentControllers(t *testing.T) {
	for _, test := range []struct {
		name               string
		installed, running bool
		version            string
		calls              []string
	}{
		{"managed", true, true, "v1.2.2", nil},
		{"stopped", false, false, "", []string{"status"}},
		{"current", false, true, "v1.2.3", []string{"status"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			units := &updateControllerUnits{installed: test.installed}
			lifecycle := &daemonLifecycleFake{status: wingd.LocalStatus{Running: test.running, PID: 42, Version: test.version}}
			manager := updateLocalUnits{ManagedUnitManager: units, services: dashboardServices{lifecycle: lifecycle}}
			if err := manager.Refresh(t.Context(), filepath.Join(t.TempDir(), "sparkwing"), "v1.2.3"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(lifecycle.calls, test.calls) {
				t.Fatalf("controller calls=%v want=%v", lifecycle.calls, test.calls)
			}
		})
	}
}

func TestUpdatePropagatesControllerRefreshFailures(t *testing.T) {
	failure := errors.New("refresh failed")
	for _, stage := range []string{"units", "stop", "start"} {
		t.Run(stage, func(t *testing.T) {
			units := &updateControllerUnits{}
			lifecycle := &daemonLifecycleFake{status: wingd.LocalStatus{Running: true, PID: 42, Version: "v1.2.2"}, failAt: stage, err: failure}
			if stage == "units" {
				units.err = failure
			}
			manager := updateLocalUnits{ManagedUnitManager: units, services: dashboardServices{lifecycle: lifecycle, options: wingd.LocalStartOptions{Home: t.TempDir()}}}
			if err := manager.Refresh(t.Context(), filepath.Join(t.TempDir(), "sparkwing"), "v1.2.3"); !errors.Is(err, failure) {
				t.Fatalf("refresh error=%v want=%v", err, failure)
			}
			if stage == "units" && len(lifecycle.calls) != 0 {
				t.Fatalf("failed unit refresh touched controller: %v", lifecycle.calls)
			}
		})
	}
}
