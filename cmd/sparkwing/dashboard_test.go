package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWaitForListenerOrExit_FailsFastOnEarlyExit ensures a supervisor
// that exits during startup is reported immediately, not after the full
// generous deadline.
func TestWaitForListenerOrExit_FailsFastOnEarlyExit(t *testing.T) {
	exited := make(chan struct{})
	close(exited)

	start := time.Now()
	err := waitForListenerOrExit("127.0.0.1:1", exited, 10*time.Second)
	if err == nil {
		t.Fatal("expected an error for a dead supervisor")
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("error = %q, want the early-exit message", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("fast-fail took %s; should return well before the deadline", elapsed)
	}
}

// TestWaitForListenerOrExit_TimesOutWhenAliveButSilent verifies the
// generous-deadline path: a live process that never binds trips the
// timeout, not the early-exit path.
func TestWaitForListenerOrExit_TimesOutWhenAliveButSilent(t *testing.T) {
	exited := make(chan struct{})
	err := waitForListenerOrExit("127.0.0.1:1", exited, 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "failed to accept connections") {
		t.Fatalf("error = %v, want a deadline timeout", err)
	}
}

func TestDashboardIsNewer(t *testing.T) {
	cases := []struct {
		name    string
		running string
		mine    string
		want    bool
	}{
		{"running strictly newer", "v0.17.0", "v0.16.0", true},
		{"running older", "v0.15.0", "v0.16.0", false},
		{"equal", "v0.16.0", "v0.16.0", false},
		{"running unknown", "(unknown)", "v0.16.0", false},
		{"mine unknown", "v0.17.0", "(unknown)", false},
		{"both dev pseudo", "v0.0.0-dev", "v0.0.0-dev", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dashboardIsNewer(c.running, c.mine); got != c.want {
				t.Errorf("dashboardIsNewer(%q, %q) = %v, want %v", c.running, c.mine, got, c.want)
			}
		})
	}
}

// TestProbeDashboardVersion_FallsBackToAddr checks the handshake reaches
// a dashboard by its bind address when dev.env is absent.
func TestProbeDashboardVersion_FallsBackToAddr(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"v0.17.0","schema":11,"pid":42}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	info, ok := probeDashboardVersion(t.TempDir(), addr)
	if !ok {
		t.Fatal("probe should succeed against a live version endpoint")
	}
	if info.Version != "v0.17.0" {
		t.Errorf("version = %q, want v0.17.0", info.Version)
	}
}

// TestProbeDashboardVersion_MissingEndpoint returns ok=false for an
// older dashboard that predates the version endpoint (404), so start
// treats it as replaceable.
func TestProbeDashboardVersion_MissingEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if _, ok := probeDashboardVersion(t.TempDir(), addr); ok {
		t.Error("probe should report not-ok when the endpoint is absent")
	}
}

// TestTailFileFrom_OnlyNewInstanceLines is the deadline-message source
// guarantee: the tail after a recorded offset excludes the previous
// instance's log lines.
func TestTailFileFrom_OnlyNewInstanceLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.log")
	prev := "old request line 1\nold request line 2\n"
	if err := os.WriteFile(path, []byte(prev), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	offset := fileSize(path)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString("new supervisor: failed to bind\nnew supervisor: exiting\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	got := tailFileFrom(path, offset, 40)
	if strings.Contains(got, "old request line") {
		t.Errorf("tail leaked previous-instance lines: %q", got)
	}
	if !strings.Contains(got, "new supervisor: failed to bind") {
		t.Errorf("tail missing new-instance lines: %q", got)
	}
}

// TestTailFileFrom_EmptyWhenNoNewOutput returns empty when the new
// instance wrote nothing past the offset.
func TestTailFileFrom_EmptyWhenNoNewOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.log")
	if err := os.WriteFile(path, []byte("only old lines\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := tailFileFrom(path, fileSize(path), 40); got != "" {
		t.Errorf("tail = %q, want empty", got)
	}
}
