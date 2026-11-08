package runtime

import (
	"context"
	"testing"
)

func TestCleanupUsesTheReleaseContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	called := false
	ctx = WithCleanups(ctx, func([]string, string) (string, error) { return "resource", nil }, func(ctx context.Context, id string) error {
		called = true
		if id != "resource" {
			t.Fatalf("id=%q", id)
		}
		return ctx.Err()
	})
	cancel()
	if err := Release(context.WithoutCancel(ctx), "resource"); err != nil || !called {
		t.Fatalf("release=%v called=%v", err, called)
	}
}
