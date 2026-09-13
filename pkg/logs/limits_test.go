package logs_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

// safety: step-a and step-b hash to different append shards, which TestAppendLockShardsPerStoredFile pins.
func TestLogs_SlowBodyDoesNotBlockOtherAppends(t *testing.T) {
	for _, tc := range []struct {
		name string
		fast string
	}{
		{"same node", "step-a"},
		{"different shards", "step-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				req, rerr := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/logs/run-1/"+tc.fast,
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
		})
	}
}

func TestLogs_ConcurrentAppendsHoldTheByteCaps(t *testing.T) {
	const (
		capBytes = 1024
		writers  = 64
	)
	cases := []struct {
		name   string
		limits logs.Limits
		node   func(i int) string
	}{
		{"node cap", logs.Limits{MaxNodeBytes: capBytes}, func(int) string { return "step-a" }},
		{"run cap", logs.Limits{MaxRunBytes: capBytes}, func(i int) string { return fmt.Sprintf("step-%d", i) }},
		{"node id aliases of one file", logs.Limits{MaxNodeBytes: capBytes}, func(i int) string {
			if i%2 == 0 {
				return "a/b"
			}
			return "a__b"
		}},
	}
	body := bytes.Repeat([]byte("x"), 4096)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c, dir, stop := newLimitedServer(t, tc.limits)
			defer stop()

			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_ = c.Append(context.Background(), "run-1", tc.node(i), body)
				}()
			}
			close(start)
			wg.Wait()

			stored := readRun(t, dir, "run-1")
			markers := strings.Count(stored, logs.TruncationMarker) * len(logs.TruncationMarker)
			if payload := len(stored) - markers; payload > capBytes {
				t.Errorf("stored %d payload bytes across %d concurrent appends, want at most the %d-byte cap",
					payload, writers, capBytes)
			}
		})
	}
}

func TestLogs_ConcurrentAttemptSubstreamsShareTheNodeCap(t *testing.T) {
	const (
		capBytes = 1024
		writers  = 64
	)
	_, client, dir, stop := newLimitedServer(t, logs.Limits{MaxNodeBytes: capBytes})
	defer stop()
	body := bytes.Repeat([]byte("x"), 4096)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			generation := int64(i%2 + 1)
			ctx := store.WithNodeClaimFence(context.Background(), store.NodeClaimFence{
				HolderID: "holder", ClaimGeneration: generation,
			})
			ctx = store.WithExecutionAttemptOrdinal(ctx, int(generation))
			_ = client.Append(ctx, "run-1", "step-a", body)
		}()
	}
	close(start)
	wg.Wait()
	payload := 0
	err := filepath.WalkDir(filepath.Join(dir, "runs", "run-1", ".attempts"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		payload += len(data) - strings.Count(string(data), logs.TruncationMarker)*len(logs.TruncationMarker)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload > capBytes {
		t.Fatalf("stored %d payload bytes across attempt streams, want at most %d", payload, capBytes)
	}
}

func TestLogs_InFlightBudgetRefusesExcessAppends(t *testing.T) {
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.WithLimits(logs.Limits{MaxInFlightBytes: 8192})
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
	if _, err := pw.Write([]byte("holding the budget\n")); err != nil {
		t.Fatal(err)
	}

	status := 0
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		resp, perr := http.Post(srv.URL+"/api/v1/logs/run-1/step-b", "text/plain", strings.NewReader("line\n"))
		if perr != nil {
			t.Fatalf("second append: %v", perr)
		}
		status = resp.StatusCode
		_ = resp.Body.Close()
		if status == http.StatusServiceUnavailable {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503 while a stalled body holds the whole in-flight budget", status)
	}

	_ = pw.Close()
	<-slowDone
}

func TestLogs_SweepSparesRunsWrittenNearTheCutoff(t *testing.T) {
	s, c, dir, stop := newLimitedServer(t, logs.Limits{Retention: time.Hour, SweepInterval: 10 * time.Minute})
	defer stop()

	if err := c.Append(context.Background(), "run-1", "step-a", []byte("line\n")); err != nil {
		t.Fatal(err)
	}
	near := time.Now().Add(-65 * time.Minute)
	run := filepath.Join(dir, "runs", "run-1")
	if err := os.Chtimes(filepath.Join(run, "step-a.log"), near, near); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(run, near, near); err != nil {
		t.Fatal(err)
	}

	removed, err := s.SweepOnce(time.Now())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d, want a run written within one sweep of the cutoff to wait a sweep", removed)
	}
	if _, err := os.Stat(run); err != nil {
		t.Errorf("run removed while an append could still be in flight: %v", err)
	}
}

func TestLogs_LineCapTruncatesTheLongLineAndKeepsTheRest(t *testing.T) {
	const cap = 64
	_, c, dir, stop := newLimitedServer(t, logs.Limits{MaxLineBytes: cap})
	defer stop()

	body := []byte("short\n" + strings.Repeat("L", 200) + "\nafter\n")
	if err := c.Append(context.Background(), "run-1", "step-a", body); err != nil {
		t.Fatalf("append: %v", err)
	}

	stored := readRun(t, dir, "run-1")
	if !strings.Contains(stored, "short\n") {
		t.Errorf("the line under the cap was not stored:\n%s", stored)
	}
	if !strings.Contains(stored, "after\n") {
		t.Errorf("the line after the long one was dropped:\n%s", stored)
	}
	if strings.Count(stored, logs.LineTruncationMarker) != 1 {
		t.Errorf("want one line-truncation marker, got:\n%s", stored)
	}
	for _, line := range strings.SplitAfter(stored, "\n") {
		if len(line) > cap {
			t.Errorf("stored line of %d bytes exceeds the %d-byte cap: %q", len(line), cap, line)
		}
	}
}

func TestLogs_LineCapMarksEveryCutLineInsideTheCap(t *testing.T) {
	const cap = 64
	_, c, dir, stop := newLimitedServer(t, logs.Limits{MaxLineBytes: cap})
	defer stop()

	body := []byte(strings.Repeat("A", 300) + "\n" + strings.Repeat("B", 300) + "\nshort\n" +
		strings.Repeat("C", 300) + "\n")
	if err := c.Append(context.Background(), "run-1", "step-a", body); err != nil {
		t.Fatalf("append: %v", err)
	}

	stored := readRun(t, dir, "run-1")
	if got := strings.Count(stored, logs.LineTruncationMarker); got != 3 {
		t.Errorf("three cut lines earned %d markers, want one each:\n%s", got, stored)
	}
	for _, line := range strings.SplitAfter(stored, "\n") {
		if len(line) > cap {
			t.Errorf("stored line of %d bytes exceeds the %d-byte cap: %q", len(line), cap, line)
		}
	}
	if !strings.Contains(stored, "short\n") {
		t.Errorf("the short line between two cut ones was lost:\n%s", stored)
	}
	for _, want := range []string{"AAA", "BBB", "CCC"} {
		if !strings.Contains(stored, want) {
			t.Errorf("the append lost its %s line:\n%s", want, stored)
		}
	}
}

func TestLogs_LineCapUnderTheMarkerIsRaisedToAUsableOne(t *testing.T) {
	_, c, dir, stop := newLimitedServer(t, logs.Limits{MaxLineBytes: 8})
	defer stop()

	if err := c.Append(context.Background(), "run-1", "step-a", []byte(strings.Repeat("L", 400)+"\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	stored := readRun(t, dir, "run-1")
	if !strings.Contains(stored, logs.LineTruncationMarker) {
		t.Errorf("a cap under the marker stored a cut line with no marker:\n%q", stored)
	}
	if int64(len(stored)) > logs.MinLineBytes {
		t.Errorf("stored %d bytes, want at most the raised cap of %d", len(stored), logs.MinLineBytes)
	}
}

func TestLogs_LineCapCutsOnARuneBoundary(t *testing.T) {
	const cap = 64
	_, c, dir, stop := newLimitedServer(t, logs.Limits{MaxLineBytes: cap})
	defer stop()

	body := []byte(strings.Repeat("héllo wörld ", 40) + "\n")
	if err := c.Append(context.Background(), "run-1", "step-a", body); err != nil {
		t.Fatalf("append: %v", err)
	}

	stored := readRun(t, dir, "run-1")
	if !utf8.ValidString(stored) {
		t.Errorf("the capped line is not valid UTF-8: %q", stored)
	}
	if strings.ContainsRune(stored, utf8.RuneError) {
		t.Errorf("the cut produced a replacement character: %q", stored)
	}
	if len(stored) > cap {
		t.Errorf("stored %d bytes for one capped line, want at most %d", len(stored), cap)
	}
}

func TestLogs_LineCapLeavesShortLinesByteIdentical(t *testing.T) {
	_, c, dir, stop := newLimitedServer(t, logs.Limits{MaxLineBytes: 64})
	defer stop()

	body := "one\ntwo\nthree without a newline"
	if err := c.Append(context.Background(), "run-1", "step-a", []byte(body)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if stored := readRun(t, dir, "run-1"); stored != body {
		t.Errorf("stored %q, want the body unchanged", stored)
	}
}

func TestLogs_UncappedLinesAreStoredWhole(t *testing.T) {
	_, c, dir, stop := newLimitedServer(t, logs.Limits{})
	defer stop()

	line := strings.Repeat("x", 5000) + "\n"
	if err := c.Append(context.Background(), "run-1", "step-a", []byte(line)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if stored := readRun(t, dir, "run-1"); stored != line {
		t.Errorf("stored %d bytes with no line cap, want the whole %d-byte line", len(stored), len(line))
	}
}

func TestLogs_GzipOutputReadsAsBinaryAtTheDocumentedThreshold(t *testing.T) {
	_, c, dir, stop := newLimitedServer(t, logs.Limits{BinaryRatio: 0.3})
	defer stop()

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for range 200 {
		if _, err := zw.Write([]byte("PASS ok github.com/example/pkg 0.4s\n")); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
	}
	if _, err := zw.Write(randomish(4096)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	if err := c.Append(context.Background(), "run-1", "step-a", buf.Bytes()); err != nil {
		t.Fatalf("append gzip: %v", err)
	}
	stored := readRun(t, dir, "run-1")
	if stored != logs.BinaryDropMarker {
		t.Errorf("a gzip blob stored %d bytes, want only the drop marker:\n%q", len(stored), stored)
	}
}

func randomish(n int) []byte {
	out := make([]byte, n)
	x := uint32(2166136261)
	for i := range out {
		x = x*16777619 + uint32(i)
		out[i] = byte(x >> 13)
	}
	return out
}

func TestLogs_BinaryMarkerIsWrittenOncePerNodeLogNotOncePerBurst(t *testing.T) {
	_, c, dir, stop := newLimitedServer(t, logs.Limits{BinaryRatio: 0.3})
	defer stop()
	ctx := context.Background()

	binary := make([]byte, 256)
	for i := range binary {
		binary[i] = byte(i % 5)
	}
	for range 2 {
		if err := c.Append(ctx, "run-1", "step-a", binary); err != nil {
			t.Fatalf("append binary: %v", err)
		}
		if err := c.Append(ctx, "run-1", "step-a", []byte("back to text\n")); err != nil {
			t.Fatalf("append text: %v", err)
		}
	}

	stored := readRun(t, dir, "run-1")
	if got := strings.Count(stored, logs.BinaryDropMarker); got != 1 {
		t.Errorf("the node log carries %d drop markers across two binary bursts, want 1:\n%q", got, stored)
	}
	if got := strings.Count(stored, "back to text\n"); got != 2 {
		t.Errorf("text appends between binary bursts were lost: %q", stored)
	}
}

func TestLogs_BinaryOutputIsDroppedAfterOneWarningLine(t *testing.T) {
	_, c, dir, stop := newLimitedServer(t, logs.Limits{BinaryRatio: 0.3})
	defer stop()
	ctx := context.Background()

	binary := make([]byte, 512)
	for i := range binary {
		binary[i] = byte(i % 7)
	}
	for range 3 {
		if err := c.Append(ctx, "run-1", "step-a", binary); err != nil {
			t.Fatalf("append binary: %v", err)
		}
	}

	stored := readRun(t, dir, "run-1")
	if strings.Count(stored, logs.BinaryDropMarker) != 1 {
		t.Errorf("want one binary-drop marker across three binary appends, got:\n%q", stored)
	}
	if strings.Contains(stored, "\x00\x01\x02") {
		t.Errorf("binary output reached the stored log:\n%q", stored)
	}
	if int64(len(stored)) != int64(len(logs.BinaryDropMarker)) {
		t.Errorf("stored %d bytes, want only the %d-byte marker", len(stored), len(logs.BinaryDropMarker))
	}
}

func TestLogs_TextWithEscapeSequencesIsNotMistakenForBinary(t *testing.T) {
	_, c, dir, stop := newLimitedServer(t, logs.Limits{BinaryRatio: 0.3})
	defer stop()

	body := "\x1b[32mPASS\x1b[0m ok\tgithub.com/example/pkg\t0.4s\nrésumé built\n"
	if err := c.Append(context.Background(), "run-1", "step-a", []byte(body)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if stored := readRun(t, dir, "run-1"); stored != body {
		t.Errorf("colored UTF-8 output was altered:\nstored %q\nwant   %q", stored, body)
	}
}

func TestLogs_BinaryDetectionIsOffByDefault(t *testing.T) {
	_, c, dir, stop := newLimitedServer(t, logs.Limits{})
	defer stop()

	binary := []byte{0, 1, 2, 3, 0, 1, 2, 3}
	if err := c.Append(context.Background(), "run-1", "step-a", binary); err != nil {
		t.Fatalf("append: %v", err)
	}
	if stored := readRun(t, dir, "run-1"); stored != string(binary) {
		t.Errorf("stored %q with detection off, want the bytes as sent", stored)
	}
}

type ceilingServer struct {
	server *logs.Server
	client *logs.Client
	dir    string
	url    string
}

func newCeilingServer(t *testing.T, ceiling objectguard.CeilingConfig) ceilingServer {
	t.Helper()
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.WithStoreCeiling(ceiling)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return ceilingServer{server: s, client: logs.NewClient(srv.URL, nil), dir: dir, url: srv.URL}
}

func TestLogs_StoreCeilingRefusesAppendsAndNamesTheLimit(t *testing.T) {
	fix := newCeilingServer(t, objectguard.CeilingConfig{
		Limit: objectguard.CeilingLimit{MaxBytes: 64},
	})
	s, c, dir := fix.server, fix.client, fix.dir
	ctx := context.Background()

	if err := c.Append(ctx, "run-1", "step-a", []byte(strings.Repeat("x", 128)+"\n")); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := s.MeasureStore(ctx); err != nil {
		t.Fatalf("MeasureStore: %v", err)
	}
	if !s.StoreCeiling().Frozen {
		t.Fatal("a store over its ceiling did not freeze")
	}

	resp, err := http.Post(fix.url+"/api/v1/logs/run-1/step-a", "text/plain", strings.NewReader("more\n"))
	if err != nil {
		t.Fatalf("post append: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("append over the store ceiling got %d, want 507: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"storage ceiling reached", logs.StoreCeilingSubject, "64", "--max-store-bytes"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the refusal %q does not name %q", body, want)
		}
	}

	stored := readRun(t, dir, "run-1")
	if strings.Contains(stored, "more") {
		t.Errorf("the refused append reached the store:\n%q", stored)
	}
}

func TestLogs_StoreCeilingThawsWhenTheSweepFreesSpace(t *testing.T) {
	fix := newCeilingServer(t, objectguard.CeilingConfig{
		Limit: objectguard.CeilingLimit{MaxObjects: 2},
	})
	s, c := fix.server, fix.client
	ctx := context.Background()

	for _, node := range []string{"step-a", "step-b"} {
		if err := c.Append(ctx, "run-1", node, []byte("hello\n")); err != nil {
			t.Fatalf("append %s: %v", node, err)
		}
	}
	if err := s.MeasureStore(ctx); err != nil {
		t.Fatalf("MeasureStore: %v", err)
	}
	if !s.StoreCeiling().Frozen {
		t.Fatalf("a store at its object ceiling did not freeze: %+v", s.StoreCeiling())
	}
	if err := c.DeleteRun(ctx, "run-1"); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if err := s.MeasureStore(ctx); err != nil {
		t.Fatalf("MeasureStore: %v", err)
	}
	if s.StoreCeiling().Frozen {
		t.Error("the measurement after a delete left the store frozen")
	}
	if err := c.Append(ctx, "run-2", "step-a", []byte("after\n")); err != nil {
		t.Errorf("append after the store fell back under its ceiling: %v", err)
	}
}

func TestLogs_StoreCeilingIsOffByDefault(t *testing.T) {
	fix := newCeilingServer(t, objectguard.CeilingConfig{})
	s, c := fix.server, fix.client

	for range 3 {
		if err := c.Append(context.Background(), "run-1", "step-a", []byte(strings.Repeat("x", 4096))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if state := s.StoreCeiling(); state.Enforced || state.Frozen {
		t.Errorf("an unconfigured store reports %+v", state)
	}
}

func TestLogs_HealthCarriesTheStoreCeiling(t *testing.T) {
	fix := newCeilingServer(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 64, WarnBytes: 16},
		Reconcile: time.Hour,
	})
	ctx := context.Background()

	if err := fix.client.Append(ctx, "run-1", "step-a", []byte(strings.Repeat("x", 32)+"\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := fix.server.MeasureStore(ctx); err != nil {
		t.Fatalf("MeasureStore: %v", err)
	}

	warned := healthBody(t, fix.url)
	ceiling, ok := warned["store_ceiling"].(map[string]any)
	if !ok {
		t.Fatalf("health carries no store_ceiling: %v", warned)
	}
	if ceiling["enforced"] != true || ceiling["warning"] != true || ceiling["frozen"] != false {
		t.Errorf("a store past its warning mark reports %v", ceiling)
	}
	if ceiling["bytes"].(float64) <= 0 || ceiling["objects"].(float64) != 1 {
		t.Errorf("health reports %v bytes / %v objects", ceiling["bytes"], ceiling["objects"])
	}
	if _, ok := ceiling["reconciled_at"]; !ok {
		t.Error("health reports no reconciled_at after a measurement")
	}
	if warned["status"] != "degraded" {
		t.Errorf("status = %v past the warning mark, want degraded", warned["status"])
	}

	if err := fix.client.Append(ctx, "run-1", "step-a", []byte(strings.Repeat("y", 128)+"\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := fix.server.MeasureStore(ctx); err != nil {
		t.Fatalf("MeasureStore: %v", err)
	}
	frozen := healthBody(t, fix.url)
	if frozen["store_ceiling"].(map[string]any)["frozen"] != true {
		t.Errorf("health does not report the freeze: %v", frozen["store_ceiling"])
	}
	var named bool
	for _, p := range frozen["problems"].([]any) {
		if strings.Contains(p.(string), "ceiling") {
			named = true
		}
	}
	if !named {
		t.Errorf("health problems do not name the frozen store: %v", frozen["problems"])
	}
}

func TestLogs_MetricsCarryTheStoreCeiling(t *testing.T) {
	fix := newCeilingServer(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 64, MaxObjects: 9},
		Reconcile: time.Hour,
	})
	ctx := context.Background()
	if err := fix.client.Append(ctx, "run-1", "step-a", []byte(strings.Repeat("x", 128)+"\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := fix.server.MeasureStore(ctx); err != nil {
		t.Fatalf("MeasureStore: %v", err)
	}

	resp, err := http.Get(fix.url + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"sparkwing_logs_store_bytes",
		"sparkwing_logs_store_objects",
		`sparkwing_logs_store_ceiling{unit="bytes"} 64`,
		`sparkwing_logs_store_ceiling{unit="objects"} 9`,
		"sparkwing_logs_store_ceiling_frozen 1",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics does not carry %q", want)
		}
	}
}

func TestLogs_DeletingARunRemeasuresAndThawsTheStore(t *testing.T) {
	fix := newCeilingServer(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 64},
		Reconcile: time.Hour,
	})
	ctx := context.Background()

	if err := fix.client.Append(ctx, "run-1", "step-a", []byte(strings.Repeat("x", 128)+"\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := fix.server.MeasureStore(ctx); err != nil {
		t.Fatalf("MeasureStore: %v", err)
	}
	if !fix.server.StoreCeiling().Frozen {
		t.Fatal("the store did not freeze")
	}

	if err := fix.client.DeleteRun(ctx, "run-1"); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if fix.server.StoreCeiling().Frozen {
		t.Error("deleting the only run left the store frozen, so an operator has to wait out the interval")
	}
	if err := fix.client.Append(ctx, "run-2", "step-a", []byte("after\n")); err != nil {
		t.Errorf("append after the delete freed space: %v", err)
	}
}

func healthBody(t *testing.T, base string) map[string]any {
	t.Helper()
	resp, err := http.Get(base + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return body
}
