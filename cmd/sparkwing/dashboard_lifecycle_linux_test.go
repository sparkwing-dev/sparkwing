//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func dashboardSleepingRecord(t *testing.T) (dashboardPaths, dashboardRecord, *exec.Cmd) {
	t.Helper()
	dp, err := resolveDashboardPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "60")
	if err = child.Start(); err != nil {
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
	record := dashboardRecord{PID: child.Process.Pid, Birth: birth, Boot: boot, Instance: "fixture-instance", Options: dashboardOptions{Addr: "127.0.0.1:1", ReadOnly: true}}
	if err = writeDashboardRecord(dp, record); err != nil {
		t.Fatal(err)
	}
	return dp, record, child
}

func TestServeStartPreservesEveryOwnedRunningInstance(t *testing.T) {
	for _, identity := range []string{"match", "different", "unknown"} {
		t.Run(identity, func(t *testing.T) {
			dp, record, child := dashboardSleepingRecord(t)
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			record.Artifact = dashboardFileArtifact(self)
			if identity == "different" {
				record.Artifact.SHA256 = strings.Repeat("a", 64)
			}
			if identity == "unknown" {
				record.Artifact.SHA256 = ""
			}
			if err = writeDashboardRecord(dp, record); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(dashboardStatePath(dp))
			if err != nil {
				t.Fatal(err)
			}
			output := captureStdout(t, func() {
				if err = runSparkwing([]string{"serve", "start", "--home", dp.home, "--addr", "127.0.0.1:54321", "--read-only=false", "-o", "json"}); err != nil {
					t.Fatal(err)
				}
			})
			var result dashboardResult
			if err = json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if result.PID != child.Process.Pid || result.Outcome != "already_running" || result.Bind != record.Options.Addr || !result.ReadOnly || result.Readiness != "not_ready" || result.Build.Status != identity {
				t.Fatalf("unexpected result: %+v", result)
			}
			after, err := os.ReadFile(dashboardStatePath(dp))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("start changed effective state")
			}
			if !processAlive(child.Process.Pid) {
				t.Fatal("start killed existing instance")
			}
		})
	}
}

func TestServeRefusesReusedOrOpaquePID(t *testing.T) {
	for _, kind := range []string{"reused", "opaque"} {
		t.Run(kind, func(t *testing.T) {
			dp, record, child := dashboardSleepingRecord(t)
			if kind == "reused" {
				record.Birth += "-wrong"
				if err := writeDashboardRecord(dp, record); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(dashboardStatePath(dp)); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dp.pid, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, action := range []string{"start", "stop", "restart", "status"} {
				output := captureStdout(t, func() {
					err := runSparkwing([]string{"serve", action, "--home", dp.home, "-o", "json"})
					if exitCodeFor(err) != 2 {
						t.Fatalf("%s error=%v", action, err)
					}
				})
				var result dashboardResult
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatal(err)
				}
				if result.State != "unknown" || result.Ownership != "unknown" {
					t.Fatalf("%s falsely trusted process: %+v", action, result)
				}
				if !processAlive(child.Process.Pid) {
					t.Fatalf("%s killed unrelated process", action)
				}
			}
		})
	}
}

func TestServePIDFDStopOnlyOwnedIncarnation(t *testing.T) {
	_, record, child := dashboardSleepingRecord(t)
	wrong := record
	wrong.Birth += "-reused"
	if err := stopOwnedDashboard(wrong); err == nil {
		t.Fatal("wrong incarnation accepted")
	}
	if !processAlive(child.Process.Pid) {
		t.Fatal("wrong incarnation was signaled")
	}
	if err := stopOwnedDashboard(record); err != nil {
		t.Fatal(err)
	}
	if processAlive(child.Process.Pid) {
		t.Fatal("owned child survived stop")
	}
}

func TestServeHelpAndInvalidArgumentsLeaveFreshHomeUntouched(t *testing.T) {
	for _, args := range [][]string{{"stop", "--help"}, {"restart", "--help"}, {"logs", "--help"}, {"start", "--no-local-store"}, {"start", "--addr", "malformed"}, {"stop", "--output", "wrong"}, {"start", "--output="}, {"restart", "--unexpected"}, {"logs", "--limit", "-1"}, {"kill"}, {"kill", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("SPARKWING_HOME", filepath.Join(home, "state"))
			captureStdout(t, func() { _ = runSparkwing(append([]string{"serve"}, args...)) })
			if _, err := os.Stat(filepath.Join(home, "state")); !os.IsNotExist(err) {
				t.Fatalf("input touched state: %v", err)
			}
		})
	}
}

func TestServeRefusesUnsafeStateInputsBeforeWrites(t *testing.T) {
	for _, name := range []string{"dashboard-state.json", dashboardPIDFile} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, name)
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			output := captureStdout(t, func() {
				err := runSparkwing([]string{"serve", "start", "--home", home, "-o", "json"})
				if exitCodeFor(err) != 2 {
					t.Fatalf("unsafe state: %v", err)
				}
			})
			records := decodeOutputRecords(t, []byte(output))
			if len(records) != 1 || records[0]["state"] != "unknown" {
				t.Fatal(output)
			}
			files, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatalf("unsafe state wrote files: %v", files)
			}
		})
	}
}
