package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// secretStore opens a fresh store for secret tests. Kept separate from
// newStoreT (lease_test.go) so tests run independently even if the
// lease helpers evolve.
func secretStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSecretsCRUD(t *testing.T) {
	s := secretStore(t)
	now := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)

	// Missing secret returns ErrNotFound so the CLI can map to a
	// clear error message.
	if _, err := s.GetSecret("missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSecret missing: want ErrNotFound, got %v", err)
	}

	// Initial create records both timestamps at `now`.
	if err := s.CreateOrReplaceSecret("api_token", "abc123", "alice", true, now); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}
	got, err := s.GetSecret("api_token")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != "abc123" || got.Principal != "alice" {
		t.Fatalf("GetSecret got=%+v", got)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("GetSecret timestamps: created=%v updated=%v want=%v", got.CreatedAt, got.UpdatedAt, now)
	}

	// Replace updates value + principal + updated_at but keeps
	// created_at so operators can still see when the name first
	// appeared.
	later := now.Add(5 * time.Minute)
	if err := s.CreateOrReplaceSecret("api_token", "xyz789", "bot", true, later); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err = s.GetSecret("api_token")
	if err != nil {
		t.Fatalf("GetSecret after replace: %v", err)
	}
	if got.Value != "xyz789" || got.Principal != "bot" {
		t.Fatalf("replace didn't stick: %+v", got)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(later) {
		t.Fatalf("replace timestamps: created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}

	// List returns both rows (after adding a second) ordered by name.
	if err := s.CreateOrReplaceSecret("db_password", "hunter2", "alice", true, now); err != nil {
		t.Fatalf("second create: %v", err)
	}
	secs, err := s.ListSecrets()
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(secs) != 2 {
		t.Fatalf("ListSecrets len=%d want 2", len(secs))
	}
	if secs[0].Name != "api_token" || secs[1].Name != "db_password" {
		t.Fatalf("ListSecrets order: %+v", secs)
	}

	// Delete removes the row; a follow-up delete of the same name
	// returns ErrNotFound.
	if err := s.DeleteSecret("api_token"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if err := s.DeleteSecret("api_token"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteSecret twice: want ErrNotFound, got %v", err)
	}
}
