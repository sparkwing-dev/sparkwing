package controller_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// TestSecretsRoundTrip exercises the full HTTP surface: set, get, list
// (ensuring values are blanked), delete, and the 404 path. Uses the
// shared newTestServer helper (no auth, so every request passes the
// pass-through authenticator).
func TestSecretsRoundTrip(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	c := client.New(base, nil)
	ctx := context.Background()

	if err := c.CreateSecret(ctx, "api_token", "abc123", true); err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	got, err := c.GetSecret(ctx, "api_token")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != "abc123" {
		t.Fatalf("GetSecret value=%q want abc123", got.Value)
	}
	// Auth is disabled in newTestServer, so the handler stamps
	// "anonymous" as the principal.
	if got.Principal != "anonymous" {
		t.Fatalf("GetSecret principal=%q want anonymous", got.Principal)
	}

	// List must NOT leak the raw value -- that is the entire reason
	// the list and get endpoints are split.
	secs, err := c.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(secs) != 1 {
		t.Fatalf("ListSecrets len=%d want 1", len(secs))
	}
	if secs[0].Value != "" {
		t.Fatalf("ListSecrets leaked value: %q", secs[0].Value)
	}
	if secs[0].Name != "api_token" {
		t.Fatalf("ListSecrets name=%q want api_token", secs[0].Name)
	}

	// Delete returns nil; a follow-up returns ErrNotFound so the CLI
	// can surface a clear "already gone" message.
	if err := c.DeleteSecret(ctx, "api_token"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, err := c.GetSecret(ctx, "api_token"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSecret after delete: want ErrNotFound, got %v", err)
	}
}
