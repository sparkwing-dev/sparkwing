package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func TestCacheExplainJSONEmitsReport(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCacheExplain([]string{"--dir", dir, "-o", "json"})
	})
	if runErr != nil {
		t.Fatalf("cache explain: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["dir"] != dir || payload["key"] == "" {
		t.Fatalf("explain payload = %#v", payload)
	}
}

func TestCacheExplainJSONReportsParseFailureBeforeOutputFlag(t *testing.T) {
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCacheExplain([]string{"--unknown", "-ojson"})
	})
	if runErr == nil {
		t.Fatal("cache explain hid parse failure")
	}
	if out != "" {
		t.Fatalf("failure polluted stdout: %q", out)
	}
}

func seedCommandCacheEntry(t *testing.T, body string) *bincache.Lease {
	t.Helper()
	entry, err := bincache.PipelineEntry("11111111-11111111")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
		return os.WriteFile(path, []byte(body), 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func decodeCachePayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode cache report: %v", err)
	}
	if _, ok := payload["payload"]; ok {
		t.Fatal("report wrapped in envelope")
	}
	return payload
}

func TestCacheInfoJSONReportsManagedBytes(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	lease := seedCommandCacheEntry(t, "active")
	defer func() { _ = lease.Release() }()
	var runErr error
	out := captureStdout(t, func() { runErr = runCacheInfo([]string{"-o", "json"}) })
	if runErr != nil {
		t.Fatalf("cache info: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["total_bytes"] != float64(6) || payload["entries"] != float64(1) {
		t.Fatalf("status payload = %#v", payload)
	}
}

func TestCachePruneJSONReportsBoundedOutcome(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	lease := seedCommandCacheEntry(t, "remove")
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePrune([]string{"--all", "-o", "json"})
	})
	if runErr != nil {
		t.Fatalf("cache prune: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["reclaimed_entries"] != float64(1) || payload["goal_satisfied"] != true {
		t.Fatalf("prune payload = %#v", payload)
	}
}

func TestCachePruneRejectsInvalidLimit(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePrune([]string{"--max-bytes", "invalid", "-o", "json"})
	})
	if runErr == nil {
		t.Fatal("cache prune accepted an invalid byte ceiling")
	}
	if out != "" {
		t.Fatalf("failure polluted stdout: %q", out)
	}
}

func TestCachePruneJSONReportsAPIFailure(t *testing.T) {
	original := pruneCacheToLimits
	t.Cleanup(func() { pruneCacheToLimits = original })
	pruneCacheToLimits = func(context.Context, int64, int, bool) (bincache.PruneResult, error) {
		return bincache.PruneResult{}, errors.New("store unavailable")
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePrune([]string{"--all", "-o", "json"})
	})
	if runErr == nil {
		t.Fatal("cache prune hid API failure")
	}
	if out != "" {
		t.Fatalf("failure polluted stdout: %q", out)
	}
}

func TestCachePruneJSONReportsParseFailure(t *testing.T) {
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePrune([]string{"-o", "json", "--unknown"})
	})
	if runErr == nil {
		t.Fatal("cache prune hid parse failure")
	}
	if out != "" {
		t.Fatalf("failure polluted stdout: %q", out)
	}
}

func TestCachePruneJSONReportsParseFailureBeforeOutputFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown", "-o", "json"},
		{"--unknown", "-ojson"},
		{"--unknown", "-o=json"},
		{"--unknown", "--output", "json"},
		{"--unknown", "--output=json"},
	} {
		var runErr error
		out := captureStdout(t, func() {
			runErr = runCachePrune(args)
		})
		if runErr == nil {
			t.Fatalf("cache prune hid parse failure for %q", args)
		}
		if out != "" {
			t.Fatalf("failure polluted stdout: %q", out)
		}
	}
}

func TestCachePruneRejectsOutputBeforeMutation(t *testing.T) {
	original := pruneCacheToLimits
	t.Cleanup(func() { pruneCacheToLimits = original })
	calls := 0
	pruneCacheToLimits = func(context.Context, int64, int, bool) (bincache.PruneResult, error) {
		calls++
		return bincache.PruneResult{}, nil
	}
	if err := runCachePrune([]string{"--all", "-o", "bogus"}); err == nil {
		t.Fatal("cache prune accepted an invalid output format")
	}
	if calls != 0 {
		t.Fatalf("prune calls = %d, want 0 before output validation", calls)
	}
}
