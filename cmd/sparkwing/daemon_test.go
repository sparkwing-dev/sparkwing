package main

import (
	"context"
	"os"
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
