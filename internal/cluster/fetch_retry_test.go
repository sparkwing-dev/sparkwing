package cluster

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFetchPipelineSourceWithRetry_RecoversAfterTwoFailures models
// the IMP-005 happy path: the cache's background-fetch loop hasn't
// caught up on attempt 1 or 2, but the SHA is present by attempt 3.
// The retry loop must NOT surface the cryptic git error — it should
// return the eventually-good sparkwingDir.
func TestFetchPipelineSourceWithRetry_RecoversAfterTwoFailures(t *testing.T) {
	prevFn := fetchSourceFn
	prevDelay := triggerFetchRetryDelay
	prevAttempts := triggerFetchMaxAttempts
	t.Cleanup(func() {
		fetchSourceFn = prevFn
		triggerFetchRetryDelay = prevDelay
		triggerFetchMaxAttempts = prevAttempts
	})
	triggerFetchRetryDelay = 5 * time.Millisecond
	triggerFetchMaxAttempts = 3

	var calls int32
	fetchSourceFn = func(gcURL, repoURL, branch, sha, parentDir string) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return "", errors.New("git fetch --depth 1 origin abc123: exit status 128: fatal: remote error: upload-pack: not our ref abc123")
		}
		return "/tmp/extracted/.sparkwing", nil
	}

	got, err := fetchPipelineSourceWithRetry(context.Background(),
		"http://cache", "git@github.com:o/r.git", "main", "abc123", "/tmp/work",
		slog.Default(), "run-1")
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if got != "/tmp/extracted/.sparkwing" {
		t.Errorf("returned dir: got %q, want /tmp/extracted/.sparkwing", got)
	}
	if c := atomic.LoadInt32(&calls); c != 3 {
		t.Errorf("attempts: got %d, want 3", c)
	}
}

// TestFetchPipelineSourceWithRetry_ExhaustsAndRewritesError verifies
// that after every retry hits "not our ref", the caller sees a
// human-readable error pointing at gitcache lag rather than the
// raw upload-pack message — and the original error is still in the
// chain via errors.Is/As.
func TestFetchPipelineSourceWithRetry_ExhaustsAndRewritesError(t *testing.T) {
	prevFn := fetchSourceFn
	prevDelay := triggerFetchRetryDelay
	prevAttempts := triggerFetchMaxAttempts
	t.Cleanup(func() {
		fetchSourceFn = prevFn
		triggerFetchRetryDelay = prevDelay
		triggerFetchMaxAttempts = prevAttempts
	})
	triggerFetchRetryDelay = 1 * time.Millisecond
	triggerFetchMaxAttempts = 3

	underlying := errors.New("fatal: remote error: upload-pack: not our ref deadbeef")
	var calls int32
	fetchSourceFn = func(gcURL, repoURL, branch, sha, parentDir string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", underlying
	}

	_, err := fetchPipelineSourceWithRetry(context.Background(),
		"http://cache", "git@github.com:o/r.git", "main", "deadbeef", "/tmp/work",
		slog.Default(), "run-2")
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if c := atomic.LoadInt32(&calls); c != 3 {
		t.Errorf("attempts: got %d, want 3", c)
	}
	if !strings.Contains(err.Error(), "deadbeef") {
		t.Errorf("error missing SHA: %v", err)
	}
	if !strings.Contains(err.Error(), "background fetch") {
		t.Errorf("error missing operator-readable framing: %v", err)
	}
	if !errors.Is(err, underlying) {
		t.Errorf("error chain broken: errors.Is should still find the underlying fetch error; got %v", err)
	}
}

// TestFetchPipelineSourceWithRetry_FailsFastOnUnrelatedError ensures
// non-"not our ref" errors (auth, network, malformed URL, ...) bypass
// the retry — we never want to delay an obviously-broken state by 30s
// of pointless backoff.
func TestFetchPipelineSourceWithRetry_FailsFastOnUnrelatedError(t *testing.T) {
	prevFn := fetchSourceFn
	prevDelay := triggerFetchRetryDelay
	prevAttempts := triggerFetchMaxAttempts
	t.Cleanup(func() {
		fetchSourceFn = prevFn
		triggerFetchRetryDelay = prevDelay
		triggerFetchMaxAttempts = prevAttempts
	})
	triggerFetchRetryDelay = 1 * time.Second // would be obvious if hit
	triggerFetchMaxAttempts = 3

	authErr := errors.New("git fetch: Permission denied (publickey)")
	var calls int32
	fetchSourceFn = func(gcURL, repoURL, branch, sha, parentDir string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", authErr
	}

	start := time.Now()
	_, err := fetchPipelineSourceWithRetry(context.Background(),
		"http://cache", "git@github.com:o/r.git", "main", "abc", "/tmp/work",
		slog.Default(), "run-3")
	elapsed := time.Since(start)

	if !errors.Is(err, authErr) {
		t.Errorf("err: got %v, want auth error chain", err)
	}
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Errorf("expected exactly 1 attempt for non-retryable error, got %d", c)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("non-retryable error spent %v (expected fail-fast)", elapsed)
	}
}

// TestFetchPipelineSourceWithRetry_HonorsContextCancel makes sure a
// cancelled parent context stops the retry loop instead of waiting
// out the full backoff.
func TestFetchPipelineSourceWithRetry_HonorsContextCancel(t *testing.T) {
	prevFn := fetchSourceFn
	prevDelay := triggerFetchRetryDelay
	prevAttempts := triggerFetchMaxAttempts
	t.Cleanup(func() {
		fetchSourceFn = prevFn
		triggerFetchRetryDelay = prevDelay
		triggerFetchMaxAttempts = prevAttempts
	})
	triggerFetchRetryDelay = 30 * time.Second // would block the test if honored
	triggerFetchMaxAttempts = 3

	fetchSourceFn = func(gcURL, repoURL, branch, sha, parentDir string) (string, error) {
		return "", errors.New("not our ref abc")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := fetchPipelineSourceWithRetry(ctx,
		"http://cache", "git@github.com:o/r.git", "main", "abc", "/tmp/work",
		slog.Default(), "run-4")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected ctx.Err on cancellation")
	}
	if elapsed > 5*time.Second {
		t.Errorf("retry didn't honor context: elapsed=%v", elapsed)
	}
}
