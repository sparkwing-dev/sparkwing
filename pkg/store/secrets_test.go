package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSecretsCRUD(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)

	if _, err := s.GetSecret("missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSecret missing: want ErrNotFound, got %v", err)
	}

	if err := s.CreateOrReplaceSecret(store.Secret{Name: "api_token", Value: "abc123", Principal: "alice", Masked: true}, now); err != nil {
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

	later := now.Add(5 * time.Minute)
	if err := s.CreateOrReplaceSecret(store.Secret{Name: "api_token", Value: "xyz789", Principal: "bot", Masked: true}, later); err != nil {
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

	if err := s.CreateOrReplaceSecret(store.Secret{Name: "db_password", Value: "hunter2", Principal: "alice", Masked: true}, now); err != nil {
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

	if err := s.DeleteSecret("api_token", ""); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if err := s.DeleteSecret("api_token", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteSecret twice: want ErrNotFound, got %v", err)
	}
}
