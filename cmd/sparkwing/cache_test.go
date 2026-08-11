package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/cachepressure"
)

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
	var envelope struct {
		Payload map[string]any `json:"payload"`
		Error   any            `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode cache output %q: %v", raw, err)
	}
	if envelope.Error != nil {
		t.Fatalf("cache output error = %#v", envelope.Error)
	}
	return envelope.Payload
}

func TestCacheStatusJSONReportsActiveBytes(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	lease := seedCommandCacheEntry(t, "active")
	defer func() { _ = lease.Release() }()
	var runErr error
	out := captureStdout(t, func() { runErr = runCacheStatus([]string{"-o", "json"}) })
	if runErr != nil {
		t.Fatalf("cache status: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["observed_bytes"] != float64(6) || payload["active_bytes"] != float64(6) || payload["active_entries"] != float64(1) {
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
		runErr = runCachePrune([]string{"--goal-bytes", "1", "--max-entries", "1", "-o", "json"})
	})
	if runErr != nil {
		t.Fatalf("cache prune: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["reclaimed_bytes"] != float64(6) || payload["reclaimed_entries"] != float64(1) || payload["goal_satisfied"] != true {
		t.Fatalf("prune payload = %#v", payload)
	}
}

func TestCachePruneRejectsInvalidBounds(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePrune([]string{"--goal-bytes", "0", "--max-entries", "1", "-o", "json"})
	})
	if runErr == nil {
		t.Fatal("cache prune accepted a zero byte goal")
	}
	var envelope struct {
		Payload any `json:"payload"`
		Error   any `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", out, err)
	}
	if envelope.Payload != nil || envelope.Error == nil {
		t.Fatalf("error envelope = %#v", envelope)
	}
}

func TestCachePruneJSONReportsAPIFailure(t *testing.T) {
	original := pruneCachePressure
	t.Cleanup(func() { pruneCachePressure = original })
	pruneCachePressure = func(context.Context, cachepressure.PruneOptions) (cachepressure.PruneResult, error) {
		return cachepressure.PruneResult{}, errors.New("store unavailable")
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePrune([]string{"--goal-bytes", "1", "--max-entries", "1", "-o", "json"})
	})
	if runErr == nil {
		t.Fatal("cache prune hid API failure")
	}
	var envelope struct {
		Payload any            `json:"payload"`
		Error   map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", out, err)
	}
	if envelope.Payload != nil || envelope.Error["message"] != "cache prune: store unavailable" {
		t.Fatalf("error envelope = %#v", envelope)
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
	var envelope struct {
		Payload any            `json:"payload"`
		Error   map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", out, err)
	}
	if envelope.Payload != nil || envelope.Error["message"] == nil {
		t.Fatalf("error envelope = %#v", envelope)
	}
}

func TestCachePruneRejectsOutputBeforeMutation(t *testing.T) {
	original := pruneCachePressure
	t.Cleanup(func() { pruneCachePressure = original })
	calls := 0
	pruneCachePressure = func(context.Context, cachepressure.PruneOptions) (cachepressure.PruneResult, error) {
		calls++
		return cachepressure.PruneResult{}, nil
	}
	if err := runCachePrune([]string{"--goal-bytes", "1", "--max-entries", "1", "-o", "bogus"}); err == nil {
		t.Fatal("cache prune accepted an invalid output format")
	}
	if calls != 0 {
		t.Fatalf("prune calls = %d, want 0 before output validation", calls)
	}
}
