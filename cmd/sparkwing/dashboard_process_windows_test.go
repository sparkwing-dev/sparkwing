//go:build windows

package main

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestWindowsDashboardProcessHelper(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_DASHBOARD_CHILD") != "1" {
		return
	}
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	if _, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil {
		t.Fatal(err)
	}
}

func windowsDashboardChild(t *testing.T) (dashboardRecord, *exec.Cmd) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestWindowsDashboardProcessHelper$")
	child.Env = append(os.Environ(), "SPARKWING_TEST_DASHBOARD_CHILD=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	birth, err := procgroup.ProcessBirth(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	boot, err := dashboardBoot()
	if err != nil {
		t.Fatal(err)
	}
	return dashboardRecord{PID: child.Process.Pid, Birth: birth, Boot: boot, Instance: "native-fixture"}, child
}

func TestWindowsDashboardBootIdentityIsStable(t *testing.T) {
	first, err := dashboardBoot()
	if err != nil {
		t.Fatal(err)
	}
	second, err := dashboardBoot()
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("boot identities: %q %q", first, second)
	}
}

func TestWindowsDashboardStopVerifiesProcessIncarnation(t *testing.T) {
	for _, wrongField := range []string{"birth", "boot"} {
		t.Run(wrongField, func(t *testing.T) {
			record, child := windowsDashboardChild(t)
			wrong := record
			if wrongField == "birth" {
				wrong.Birth += "-wrong"
			} else {
				wrong.Boot += "-wrong"
			}
			if err := stopOwnedDashboard(wrong); err == nil {
				t.Fatal("mismatched process identity was accepted")
			}
			if !processAlive(child.Process.Pid) {
				t.Fatal("mismatched dashboard process was stopped")
			}
			if err := stopOwnedDashboard(record); err != nil {
				t.Fatal(err)
			}
			if processAlive(child.Process.Pid) {
				t.Fatal("verified dashboard process remains alive")
			}
			if err := stopOwnedDashboard(record); err != nil {
				t.Fatalf("repeat stop: %v", err)
			}
		})
	}
}

func TestWindowsDashboardRunningArtifactIdentifiesExecutable(t *testing.T) {
	artifact := dashboardRunningArtifact()
	if artifact.Path == "" || len(artifact.SHA256) != 64 {
		t.Fatalf("running artifact: %+v", artifact)
	}
}

func TestWindowsDashboardStateRoundTripIsPrivate(t *testing.T) {
	paths, err := resolveDashboardPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := dashboardRecord{PID: os.Getpid(), Birth: "birth", Boot: "boot", Instance: "instance"}
	for _, instance := range []string{"first", "second"} {
		record.Instance = instance
		if err := writeDashboardRecord(paths, record); err != nil {
			t.Fatal(err)
		}
		actual, err := readDashboardRecord(paths)
		if err != nil {
			t.Fatal(err)
		}
		if actual.Instance != instance {
			t.Fatalf("state instance = %q", actual.Instance)
		}
	}
	entries, err := os.ReadDir(paths.home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "dashboard-state.json" {
		t.Fatalf("temporary dashboard artifacts remain: %v", entries)
	}
}
