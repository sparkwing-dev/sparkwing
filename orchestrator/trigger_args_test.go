package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestResolveTriggerArgs covers the ISS-041 contract: on retry-of, the
// resolver returns the *original* run's args, not the args passed on the
// retry trigger. Pre-fix this returned trigger.Args unconditionally and
// caused the v0.52.0 -> v0.53.0 publish skew.
func TestResolveTriggerArgs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	backend := localState{st: st}

	origArgs := map[string]string{"version": "v1", "bump": "minor"}
	if err := st.CreateRun(ctx, store.Run{
		ID:       "run-original",
		Pipeline: "release",
		Status:   "success",
		Args:     origArgs,
	}); err != nil {
		t.Fatalf("create original run: %v", err)
	}

	t.Run("fresh trigger uses invocation args", func(t *testing.T) {
		trig := &store.Trigger{
			ID:       "run-fresh",
			Pipeline: "release",
			Args:     map[string]string{"version": "v2"},
		}
		got := resolveTriggerArgs(ctx, backend, trig, nil)
		if got["version"] != "v2" {
			t.Errorf("fresh trigger: version = %q, want v2 (invocation args)", got["version"])
		}
	})

	t.Run("retry-of preserves original args", func(t *testing.T) {
		trig := &store.Trigger{
			ID:       "run-retry",
			Pipeline: "release",
			RetryOf:  "run-original",
			// Retry invocation passes a different version on purpose
			// to confirm it's *ignored* in favor of the original.
			Args: map[string]string{"version": "v99"},
		}
		got := resolveTriggerArgs(ctx, backend, trig, nil)
		if got["version"] != "v1" {
			t.Errorf("retry-of: version = %q, want v1 (original run args)", got["version"])
		}
		if got["bump"] != "minor" {
			t.Errorf("retry-of: bump = %q, want minor (original run args)", got["bump"])
		}
	})

	t.Run("retry-of with missing original falls back to invocation args", func(t *testing.T) {
		trig := &store.Trigger{
			ID:       "run-retry-orphan",
			Pipeline: "release",
			RetryOf:  "run-does-not-exist",
			Args:     map[string]string{"version": "vfallback"},
		}
		got := resolveTriggerArgs(ctx, backend, trig, nil)
		if got["version"] != "vfallback" {
			t.Errorf("retry-of with missing original: version = %q, want vfallback (fallback to invocation args)", got["version"])
		}
	})

	t.Run("nil state returns invocation args", func(t *testing.T) {
		trig := &store.Trigger{
			ID:      "run-nilstate",
			RetryOf: "run-original",
			Args:    map[string]string{"version": "vinvocation"},
		}
		got := resolveTriggerArgs(ctx, nil, trig, nil)
		if got["version"] != "vinvocation" {
			t.Errorf("nil state: version = %q, want vinvocation", got["version"])
		}
	})
}
