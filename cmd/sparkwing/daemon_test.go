package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/supervise"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestShutdownWindowsNest(t *testing.T) {
	if orchestrator.FinalizeTimeout >= wingd.FinalizeDrainWindow {
		t.Errorf("one finalize may take %s but the drain waits %s, so the drain cannot outlast a finalize",
			orchestrator.FinalizeTimeout, wingd.FinalizeDrainWindow)
	}
	if wingd.FinalizeDrainWindow > supervise.DefaultTermGrace {
		t.Errorf("the drain waits %s but the supervisor kills after %s, so the drain is unreachable under supervision",
			wingd.FinalizeDrainWindow, supervise.DefaultTermGrace)
	}
	if supervise.DefaultTermGrace >= daemonRestartTimeout {
		t.Errorf("the supervisor takes up to %s to stop a daemon but a restart gives up after %s",
			supervise.DefaultTermGrace, daemonRestartTimeout)
	}
}

func TestInspectDaemonReportsExactSourceRevision(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sparkwing-daemon-status-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	ctx, cancel := context.WithCancel(context.Background())
	d, err := wingd.New(wingd.Config{Home: home, Version: "v0.22.2-dev+12345678"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		<-done
	})

	report, err := inspectDaemon(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Running || !report.Healthy || report.BinaryVersion != "v0.22.2-dev+12345678" || report.RunningRevision != "12345678" {
		t.Fatalf("daemon report = %+v", report)
	}
}

func TestInspectDaemonLeavesAbsentHomeStopped(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sparkwing-daemon-stopped-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	report, err := inspectDaemon(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if report.Running || report.Healthy {
		t.Fatalf("absent daemon report = %+v", report)
	}
}

func TestDaemonStatusPipedDefaultIsJSON(t *testing.T) {
	home := t.TempDir()
	var runErr error
	out := captureStdout(t, func() {
		runErr = runDaemon([]string{"status", "--home", home})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var report daemonReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("default output %q is not JSON: %v", out, err)
	}
	if report.Running || report.Socket == "" {
		t.Fatalf("status report = %+v", report)
	}
}

func TestDaemonStatusExplicitOutputOverridesPipe(t *testing.T) {
	home := t.TempDir()
	tests := []struct {
		format string
		want   string
	}{
		{format: "pretty", want: "wingd is stopped\n"},
		{format: "plain", want: "stopped\n"},
	}
	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			var runErr error
			out := captureStdout(t, func() {
				runErr = runDaemon([]string{"status", "--home", home, "-o", test.format})
			})
			if runErr != nil {
				t.Fatal(runErr)
			}
			if out != test.want {
				t.Fatalf("output = %q, want %q", out, test.want)
			}
		})
	}
}

func TestDaemonRestartPipedDefaultPreservesStoppedDaemon(t *testing.T) {
	home := t.TempDir()
	var runErr error
	out := captureStdout(t, func() {
		runErr = runDaemon([]string{"restart", "--home", home})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var report daemonReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("default output %q is not JSON: %v", out, err)
	}
	if report.Running || report.Restarted {
		t.Fatalf("restart report = %+v", report)
	}
}

func TestDaemonRestartDeclaresForceFlag(t *testing.T) {
	for _, spec := range cmdDaemonRestart.Flags {
		if spec.Name == "force" {
			return
		}
	}
	t.Fatal("daemon restart does not declare --force")
}

func TestDaemonRestartForceUsesForcedReplacement(t *testing.T) {
	called := ""
	deps := daemonRestartDeps{
		installedVersion: func() string { return "v0.37.4" },
		refresh: func(context.Context, wingdclient.Options) (wingdclient.RefreshResult, error) {
			called = "refresh"
			return wingdclient.RefreshResult{}, nil
		},
		restart: func(_ context.Context, opts wingdclient.Options) (wingdclient.RefreshResult, error) {
			called = "restart:" + opts.Version
			return wingdclient.RefreshResult{PreviousVersion: opts.Version, RunningVersion: opts.Version, Restarted: true}, nil
		},
		inspect: func(context.Context, string) (daemonReport, error) {
			return daemonReport{Running: true, Healthy: true, BinaryVersion: "v0.37.4"}, nil
		},
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runDaemonRestartWith([]string{"--force", "-o", "json"}, deps)
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var report daemonReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if called != "restart:v0.37.4" || !report.Running || !report.Healthy || !report.Restarted || report.BinaryVersion != "v0.37.4" || report.PreviousVersion != "v0.37.4" {
		t.Fatalf("call = %q, report = %+v", called, report)
	}
}

func TestDaemonRestartForcePreservesStoppedDaemon(t *testing.T) {
	deps := daemonRestartDeps{
		installedVersion: func() string { return "v0.37.4" },
		refresh:          wingdclient.RefreshRunning,
		restart: func(context.Context, wingdclient.Options) (wingdclient.RefreshResult, error) {
			return wingdclient.RefreshResult{}, wingdclient.ErrNoDaemon
		},
		inspect: func(context.Context, string) (daemonReport, error) {
			return daemonReport{Socket: "/tmp/stopped.sock"}, nil
		},
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runDaemonRestartWith([]string{"--force", "-o", "json"}, deps)
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var report daemonReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Running || report.Restarted {
		t.Fatalf("report = %+v", report)
	}
}

func TestDaemonCommandsRejectUnknownOutput(t *testing.T) {
	for _, subcommand := range []string{"status", "restart"} {
		err := runDaemon([]string{subcommand, "--home", t.TempDir(), "-o", "yaml"})
		if err == nil || !strings.Contains(err.Error(), "pretty|json|plain") {
			t.Fatalf("%s error = %v", subcommand, err)
		}
	}
}

func TestDaemonRecoverStateRequiresConsentAndPreservesUnreadableBytes(t *testing.T) {
	home := t.TempDir()
	stateDir := filepath.Join(home, "wingd")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateDir, "state.json")
	want := []byte("{truncated")
	if err := os.WriteFile(statePath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	err := runDaemon([]string{"recover-state", "--home", home})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("recovery without consent error = %v", err)
	}
	if got, err := os.ReadFile(statePath); err != nil || string(got) != string(want) {
		t.Fatalf("recovery without consent changed state: %q, %v", got, err)
	}

	var runErr error
	captureStdout(t, func() {
		runErr = runDaemon([]string{"recover-state", "--home", home, "--yes"})
	})
	if runErr != nil {
		t.Fatalf("recover unreadable state: %v", runErr)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("unreadable state still blocks startup: %v", err)
	}
	matches, err := filepath.Glob(statePath + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("preserved recovery files = %v, err %v", matches, err)
	}
	if got, err := os.ReadFile(matches[0]); err != nil || string(got) != string(want) {
		t.Fatalf("preserved recovery bytes = %q, %v", got, err)
	}
}

func TestInspectDaemonNamesADaemonBehindTheStoreSchema(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sparkwing-daemon-schema-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	st, err := store.Open(paths.PathsAt(home).StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d, err := wingd.New(wingd.Config{Home: home, Version: "v0.38.2", StoreSchemaVersion: store.ExpectedSchemaVersion() - 1})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		<-done
	})

	report, err := inspectDaemon(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if report.StoreSchemaVersion != store.ExpectedSchemaVersion() {
		t.Fatalf("store schema = %d, want %d", report.StoreSchemaVersion, store.ExpectedSchemaVersion())
	}
	if report.DaemonSchemaVersion != store.ExpectedSchemaVersion()-1 {
		t.Fatalf("daemon schema = %d, want %d", report.DaemonSchemaVersion, store.ExpectedSchemaVersion()-1)
	}
	if !report.SchemaDiverged || report.Healthy {
		t.Fatalf("a daemon behind the store schema reported healthy: %+v", report)
	}
}

func TestInspectDaemonAcceptsADaemonBehindByAdditiveMigrationsOnly(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sparkwing-daemon-additive-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	st, err := store.Open(paths.PathsAt(home).StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d, err := wingd.New(wingd.Config{
		Home:               home,
		Version:            "v0.38.2",
		StoreSchemaVersion: store.ExpectedSchemaVersion() - 1,
		StoreRequirements:  store.KnownRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		<-done
	})

	report, err := inspectDaemon(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaDiverged || !report.Healthy {
		t.Fatalf("a daemon that knows every store requirement reported diverged: %+v", report)
	}
	if len(report.MissingRequirements) != 0 {
		t.Fatalf("missing requirements = %v, want none", report.MissingRequirements)
	}
}

func TestInspectDaemonReportsAnUnreadableStoreAsUnhealthy(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sparkwing-daemon-badstore-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if err := os.MkdirAll(paths.PathsAt(home).StateDB(), 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := inspectDaemon(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if report.StoreSchemaError == "" {
		t.Fatalf("unreadable store reported no error: %+v", report)
	}
	if report.StoreSchemaVersion != 0 {
		t.Fatalf("unreadable store reported schema %d", report.StoreSchemaVersion)
	}
}

func TestInspectDaemonLeavesAnAbsentStoreWithoutAnError(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sparkwing-daemon-nostore-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	report, err := inspectDaemon(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if report.StoreSchemaError != "" || report.StoreSchemaVersion != 0 {
		t.Fatalf("absent store reported %+v, want a silent zero", report)
	}
}

func TestSchemaRemedyNamesWhatActuallyWorks(t *testing.T) {
	same := schemaRemedy(daemonReport{
		BinaryVersion: "v0.38.2", InstalledVersion: "v0.38.2",
		DaemonSchemaVersion: 17, StoreSchemaVersion: 26,
	})
	if !strings.Contains(same, "will not help") || !strings.Contains(same, wingdclient.HostBinEnv) || !strings.Contains(same, "26") {
		t.Fatalf("same-build remedy = %q", same)
	}

	newer := schemaRemedy(daemonReport{
		BinaryVersion: "v0.38.2", InstalledVersion: "v0.40.0",
		DaemonSchemaVersion: 17, StoreSchemaVersion: 26,
	})
	if !strings.Contains(newer, "daemon restart") || !strings.Contains(newer, "v0.40.0") {
		t.Fatalf("newer-install remedy = %q", newer)
	}
}
