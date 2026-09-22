package cluster

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/cache"
)

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
	fetchSourceFn = func(_ context.Context, gcURL, controllerURL, token, _, repoURL, branch, sha, parentDir string) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return "", errors.New("git fetch --depth 1 origin abc123: exit status 128: fatal: remote error: upload-pack: not our ref abc123")
		}
		return "/tmp/extracted/.sparkwing", nil
	}

	got, err := fetchPipelineSourceWithRetry(context.Background(),
		"http://cache", "http://controller", "token", "", "git@github.com:o/r.git", "main", "abc123", "/tmp/work",
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
	fetchSourceFn = func(_ context.Context, gcURL, controllerURL, token, _, repoURL, branch, sha, parentDir string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", underlying
	}

	_, err := fetchPipelineSourceWithRetry(context.Background(),
		"http://cache", "http://controller", "token", "", "git@github.com:o/r.git", "main", "deadbeef", "/tmp/work",
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

func TestFetchPipelineSourceWithRetry_FailsFastOnUnrelatedError(t *testing.T) {
	prevFn := fetchSourceFn
	prevDelay := triggerFetchRetryDelay
	prevAttempts := triggerFetchMaxAttempts
	t.Cleanup(func() {
		fetchSourceFn = prevFn
		triggerFetchRetryDelay = prevDelay
		triggerFetchMaxAttempts = prevAttempts
	})
	triggerFetchRetryDelay = 1 * time.Second
	triggerFetchMaxAttempts = 3

	authErr := errors.New("git fetch: Permission denied (publickey)")
	var calls int32
	fetchSourceFn = func(_ context.Context, gcURL, controllerURL, token, _, repoURL, branch, sha, parentDir string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", authErr
	}

	start := time.Now()
	_, err := fetchPipelineSourceWithRetry(context.Background(),
		"http://cache", "http://controller", "token", "", "git@github.com:o/r.git", "main", "abc", "/tmp/work",
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

func TestFetchPipelineSourceWithRetry_HonorsContextCancel(t *testing.T) {
	prevFn := fetchSourceFn
	prevDelay := triggerFetchRetryDelay
	prevAttempts := triggerFetchMaxAttempts
	t.Cleanup(func() {
		fetchSourceFn = prevFn
		triggerFetchRetryDelay = prevDelay
		triggerFetchMaxAttempts = prevAttempts
	})
	triggerFetchRetryDelay = 30 * time.Second
	triggerFetchMaxAttempts = 3

	attempted := make(chan struct{}, 1)
	fetchSourceFn = func(_ context.Context, gcURL, controllerURL, token, _, repoURL, branch, sha, parentDir string) (string, error) {
		select {
		case attempted <- struct{}{}:
		default:
		}
		return "", errors.New("not our ref abc")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		select {
		case <-attempted:
			cancel()
		case <-ctx.Done():
		}
	}()

	start := time.Now()
	_, err := fetchPipelineSourceWithRetry(ctx,
		"http://cache", "http://controller", "token", "", "git@github.com:o/r.git", "main", "abc", "/tmp/work",
		slog.Default(), "run-4")
	<-cancelDone
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("retry didn't honor context: elapsed=%v", elapsed)
	}
}

func TestAdoptBaselineWithRetry_RecoversFromALaggingMirrorThenGivesUp(t *testing.T) {
	prevFn := adoptBaselineFn
	prevDelay := triggerFetchRetryDelay
	prevAttempts := triggerFetchMaxAttempts
	t.Cleanup(func() {
		adoptBaselineFn = prevFn
		triggerFetchRetryDelay = prevDelay
		triggerFetchMaxAttempts = prevAttempts
	})
	triggerFetchRetryDelay = time.Millisecond
	triggerFetchMaxAttempts = 3
	lag := errors.New("git fetch --depth 1 origin abc: exit status 128: fatal: remote error: upload-pack: not our ref abc")
	baseline := bincache.WorkspaceBaseline{Ref: "origin/main", SHA: strings.Repeat("c", 40)}

	var calls int32
	adoptBaselineFn = func(_ context.Context, checkoutDir, gcURL, token, sha string, got bincache.WorkspaceBaseline) error {
		if got != baseline {
			t.Errorf("baseline reaching the adopt = %+v, want %+v", got, baseline)
		}
		if atomic.AddInt32(&calls, 1) < 3 {
			return lag
		}
		return nil
	}
	if err := adoptBaselineWithRetry(context.Background(), "/tmp/checkout", "http://cache", "token", "abc",
		baseline, slog.Default(), "run-1"); err != nil {
		t.Fatalf("expected the retry to recover, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}

	atomic.StoreInt32(&calls, 0)
	adoptBaselineFn = func(_ context.Context, _, _, _, _ string, _ bincache.WorkspaceBaseline) error {
		atomic.AddInt32(&calls, 1)
		return lag
	}
	err := adoptBaselineWithRetry(context.Background(), "/tmp/checkout", "http://cache", "token", "abc",
		baseline, slog.Default(), "run-1")
	if err == nil || !strings.Contains(err.Error(), notOurRefSubstr) {
		t.Fatalf("exhausted retries returned %v, want the lag error", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestAdoptBaselineWithRetry_DoesNotRetryASourceThatServesOnlyTheSnapshot(t *testing.T) {
	prevFn := adoptBaselineFn
	t.Cleanup(func() { adoptBaselineFn = prevFn })
	var calls int32
	adoptBaselineFn = func(_ context.Context, _, _, _, _ string, _ bincache.WorkspaceBaseline) error {
		atomic.AddInt32(&calls, 1)
		return bincache.ErrBaselineUnservable
	}
	err := adoptBaselineWithRetry(context.Background(), "/tmp/checkout", "http://cache", "token", "abc",
		bincache.WorkspaceBaseline{Ref: "origin/main", SHA: strings.Repeat("d", 40)}, slog.Default(), "run-1")
	if !errors.Is(err, bincache.ErrBaselineUnservable) {
		t.Fatalf("err = %v, want ErrBaselineUnservable", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestTriggerFetchRetryDelayOutlastsTheCacheFreshnessWindow(t *testing.T) {
	window := cache.DefaultConfig().FetchFreshWindow

	if triggerFetchRetryDelay <= window {
		t.Errorf("retry delay %s does not outlast the cache's %s freshness window, "+
			"so a retry reads the same refs the first attempt did and the push race stays open",
			triggerFetchRetryDelay, window)
	}
}
