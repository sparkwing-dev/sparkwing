package opsview_test

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const strayTestWait = 10 * time.Second

func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "opsview")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func serveDaemon(t *testing.T, home, version string) {
	t.Helper()
	d, err := wingd.New(wingd.Config{Home: home, Version: version})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(strayTestWait):
			t.Error("daemon did not stop")
		}
	})
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatalf("daemon exited before serving: %v", err)
	case <-time.After(strayTestWait):
		t.Fatal("daemon never became ready")
	}
}

func diagnoseHome(t *testing.T, home string) opsview.DoctorReport {
	t.Helper()
	p := paths.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), strayTestWait)
	defer cancel()
	report, err := opsview.Diagnose(ctx, p, home, "v1.0.0", true)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	return report
}

func straySockets(r opsview.DoctorReport) []string {
	socks := make([]string, 0, len(r.StrayDaemons))
	for _, d := range r.StrayDaemons {
		socks = append(socks, d.Socket)
	}
	return socks
}

func TestDiagnose_NamesADaemonBuiltFromAScratchModule(t *testing.T) {
	home := shortHome(t)
	stray := shortHome(t)
	serveDaemon(t, stray, "v0.0.0")
	straySock, err := wingd.SocketPath(stray)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}

	report := diagnoseHome(t, home)
	if !slices.Contains(straySockets(report), straySock) {
		t.Fatalf("stray daemons %v do not name the scratch-built daemon at %q", straySockets(report), straySock)
	}
	if !report.Clean() {
		t.Errorf("another home's process made this home's report unclean: %+v", report)
	}
}

func TestRenderDoctorPretty_ShowsAStrayDaemonOnAnOtherwiseHealthyHome(t *testing.T) {
	r := opsview.DoctorReport{
		StrayDaemons: []opsview.DoctorStrayDaemon{
			{Socket: "/tmp/sparkwing-0-abc123def456/d.sock", Version: "v0.0.0"},
		},
	}
	if !r.Clean() {
		t.Fatal("a report holding only a stray daemon should read clean for its own home")
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "healthy") {
		t.Errorf("healthy home lost its healthy line:\n%s", out)
	}
	if !strings.Contains(out, "/tmp/sparkwing-0-abc123def456/d.sock") {
		t.Errorf("healthy home swallowed the stray daemon warning:\n%s", out)
	}
}

func TestDiagnose_LeavesAnotherHomesReleaseDaemonAlone(t *testing.T) {
	home := shortHome(t)
	other := shortHome(t)
	serveDaemon(t, other, "v0.22.0")
	otherSock, err := wingd.SocketPath(other)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}

	report := diagnoseHome(t, home)
	if slices.Contains(straySockets(report), otherSock) {
		t.Fatalf("release-versioned daemon at %q was reported as stray", otherSock)
	}
}

func TestDiagnose_LeavesThisHomesOwnDaemonAlone(t *testing.T) {
	home := shortHome(t)
	serveDaemon(t, home, "v0.0.0")
	ownSock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}

	report := diagnoseHome(t, home)
	if slices.Contains(straySockets(report), ownSock) {
		t.Fatalf("this home's own daemon at %q was reported as stray", ownSock)
	}
}

func TestRenderDoctorPretty_ExplainsTheStrayDaemonTell(t *testing.T) {
	r := opsview.DoctorReport{
		QuarantinedLedgers: []string{"/tmp/home/wingd/state.json.corrupt-1"},
		StrayDaemons: []opsview.DoctorStrayDaemon{
			{Socket: "/tmp/sparkwing-0-abc123def456/d.sock", Version: "v0.0.0"},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"v0.0.0", "no release carries", "temp directory", "/tmp/sparkwing-0-abc123def456/d.sock"} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output does not mention %q:\n%s", want, out)
		}
	}
}
