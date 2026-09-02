package logs_test

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func newLimitedServer(t *testing.T, l logs.Limits) (*logs.Server, *logs.Client, string, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.WithLimits(l)
	srv := httptest.NewServer(s.Handler())
	return s, logs.NewClient(srv.URL, nil), dir, srv.Close
}

func TestLogs_ByteCapsTruncateWithMarker(t *testing.T) {
	cases := []struct {
		name   string
		limits logs.Limits
		nodes  []string
	}{
		{"node cap", logs.Limits{MaxNodeBytes: 16}, []string{"step-a", "step-a"}},
		{"run cap", logs.Limits{MaxRunBytes: 16}, []string{"step-a", "step-b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c, dir, stop := newLimitedServer(t, tc.limits)
			defer stop()
			ctx := context.Background()

			for _, node := range tc.nodes {
				if err := c.Append(ctx, "run-1", node, []byte("0123456789\n")); err != nil {
					t.Fatalf("append %s: %v", node, err)
				}
			}
			if err := c.Append(ctx, "run-1", tc.nodes[len(tc.nodes)-1], []byte("more\n")); err != nil {
				t.Fatalf("append past cap: %v", err)
			}

			total, marked := runBytes(t, dir, "run-1")
			if !marked {
				t.Fatalf("no truncation marker under %d bytes stored", total)
			}
			payload := total - int64(len(logs.TruncationMarker))
			if payload > 16 {
				t.Errorf("stored payload %d bytes, want at most the 16-byte cap", payload)
			}
			if strings.Count(readRun(t, dir, "run-1"), logs.TruncationMarker) != 1 {
				t.Errorf("truncation marker repeated:\n%s", readRun(t, dir, "run-1"))
			}
		})
	}
}

func runBytes(t *testing.T, dir, runID string) (int64, bool) {
	t.Helper()
	return int64(len(readRun(t, dir, runID))), strings.Contains(readRun(t, dir, runID), logs.TruncationMarker)
}

func readRun(t *testing.T, dir, runID string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "runs", runID))
	if err != nil {
		t.Fatalf("read run dir: %v", err)
	}
	var out strings.Builder
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, "runs", runID, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out.Write(data)
	}
	return out.String()
}

func TestLogs_FreeSpaceFloorRejectsAppend(t *testing.T) {
	_, c, _, stop := newLimitedServer(t, logs.Limits{MinFreeBytes: math.MaxUint64})
	defer stop()

	err := c.Append(context.Background(), "run-1", "step-a", []byte("line\n"))
	if err == nil {
		t.Fatal("append succeeded below the free-space floor")
	}
	if !strings.Contains(err.Error(), "507") {
		t.Errorf("err=%v, want a 507 rejection", err)
	}
}

func TestLogs_SweepRemovesExpiredRuns(t *testing.T) {
	s, c, dir, stop := newLimitedServer(t, logs.Limits{Retention: time.Hour, SweepInterval: time.Minute})
	defer stop()
	ctx := context.Background()

	for _, runID := range []string{"run-old", "run-new"} {
		if err := c.Append(ctx, runID, "step-a", []byte("line\n")); err != nil {
			t.Fatalf("append %s: %v", runID, err)
		}
	}
	stale := time.Now().Add(-2 * time.Hour)
	old := filepath.Join(dir, "runs", "run-old")
	if err := os.Chtimes(filepath.Join(old, "step-a.log"), stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	removed, err := s.SweepOnce(time.Now())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("expired run survived the sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", "run-new")); err != nil {
		t.Errorf("live run removed: %v", err)
	}
}

func TestLogs_DefaultLimitsLeaveRetentionOff(t *testing.T) {
	defaults := logs.DefaultLimits()
	if defaults.Retention != 0 {
		t.Fatalf("default retention=%s, want 0 so an upgrade deletes no history", defaults.Retention)
	}
	for _, bound := range []struct {
		name string
		on   bool
	}{
		{"MaxNodeBytes", defaults.MaxNodeBytes > 0},
		{"MaxRunBytes", defaults.MaxRunBytes > 0},
		{"MinFreeBytes", defaults.MinFreeBytes > 0},
		{"SearchMaxBytes", defaults.SearchMaxBytes > 0},
		{"SearchTimeout", defaults.SearchTimeout > 0},
	} {
		if !bound.on {
			t.Errorf("default %s is off, want it bounded", bound.name)
		}
	}

	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := logs.NewClient(srv.URL, nil)
	if err := c.Append(context.Background(), "run-1", "step-a", []byte("line\n")); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-9000 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "runs", "run-1", "step-a.log"), stale, stale)
	_ = os.Chtimes(filepath.Join(dir, "runs", "run-1"), stale, stale)

	removed, err := s.SweepOnce(time.Now())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d, want a stock server to delete nothing", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", "run-1")); err != nil {
		t.Errorf("stock server deleted history: %v", err)
	}
}

func TestLogs_SweepKeepsEverythingWithoutRetention(t *testing.T) {
	s, c, dir, stop := newLimitedServer(t, logs.Limits{})
	defer stop()

	if err := c.Append(context.Background(), "run-1", "step-a", []byte("line\n")); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-9000 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "runs", "run-1", "step-a.log"), stale, stale)
	_ = os.Chtimes(filepath.Join(dir, "runs", "run-1"), stale, stale)

	removed, err := s.SweepOnce(time.Now())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d want 0 with retention disabled", removed)
	}
}

func TestLogs_SlowBodyDoesNotBlockOtherAppends(t *testing.T) {
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	pr, pw := io.Pipe()
	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		req, rerr := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/logs/run-1/step-a", pr)
		if rerr != nil {
			return
		}
		resp, derr := http.DefaultClient.Do(req)
		if derr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	if _, err := pw.Write([]byte("slow start\n")); err != nil {
		t.Fatal(err)
	}

	fast := make(chan error, 1)
	go func() {
		req, rerr := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/logs/run-1/step-a",
			bytes.NewReader([]byte("fast line\n")))
		if rerr != nil {
			fast <- rerr
			return
		}
		resp, derr := http.DefaultClient.Do(req)
		if derr != nil {
			fast <- derr
			return
		}
		_ = resp.Body.Close()
		fast <- nil
	}()

	select {
	case err := <-fast:
		if err != nil {
			t.Fatalf("fast append: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fast append blocked behind the slow request body")
	}

	_ = pw.Close()
	<-slowDone
}
