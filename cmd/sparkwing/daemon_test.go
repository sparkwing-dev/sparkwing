package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

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

func TestDaemonCommandsRejectUnknownOutput(t *testing.T) {
	for _, subcommand := range []string{"status", "restart"} {
		err := runDaemon([]string{subcommand, "--home", t.TempDir(), "-o", "yaml"})
		if err == nil || !strings.Contains(err.Error(), "pretty|json|plain") {
			t.Fatalf("%s error = %v", subcommand, err)
		}
	}
}
