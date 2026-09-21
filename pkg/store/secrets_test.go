package store_test

import (
	"context"
	"errors"
	"strings"
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

func TestRotateSecretValues_RewritesEveryRowInOneTransaction(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 5, 3, 8, 0, 0, 0, time.UTC)

	rows := []store.Secret{
		{Name: "api_token", Value: "old:abc", Principal: "alice", Masked: true},
		{Name: "api_token", Value: "old:def", Principal: "alice", Pipeline: "deploy-web", Masked: true},
		{Name: "region", Value: "old:us-east-1", Principal: "bot", Shared: true},
	}
	for _, row := range rows {
		if err := s.CreateOrReplaceSecret(row, now); err != nil {
			t.Fatalf("CreateOrReplaceSecret(%s/%s): %v", row.Name, row.Pipeline, err)
		}
	}

	rotated, err := s.RotateSecretValues(context.Background(), func(sec store.Secret) (string, error) {
		return "new:" + strings.TrimPrefix(sec.Value, "old:"), nil
	})
	if err != nil {
		t.Fatalf("RotateSecretValues: %v", err)
	}
	if rotated != len(rows) {
		t.Fatalf("rotated=%d want %d", rotated, len(rows))
	}

	got, err := s.ListSecrets()
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	for _, sec := range got {
		if !strings.HasPrefix(sec.Value, "new:") {
			t.Fatalf("secret %s/%s value=%q, want the rewritten value", sec.Name, sec.Pipeline, sec.Value)
		}
		if !sec.UpdatedAt.Equal(now) {
			t.Fatalf("secret %s/%s updated_at=%v, want the rotation to leave it at %v",
				sec.Name, sec.Pipeline, sec.UpdatedAt, now)
		}
	}
	scoped, err := s.GetSecretRow("api_token", "deploy-web")
	if err != nil {
		t.Fatalf("GetSecretRow: %v", err)
	}
	if scoped.Value != "new:def" {
		t.Fatalf("pipeline-scoped value=%q, want new:def", scoped.Value)
	}
	if scoped.Principal != "alice" || !scoped.Masked {
		t.Fatalf("rotation changed columns other than value: %+v", scoped)
	}
}

func TestRotateSecretValues_LeavesEveryRowOnFailure(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 5, 3, 8, 0, 0, 0, time.UTC)

	for _, name := range []string{"first", "second", "third"} {
		if err := s.CreateOrReplaceSecret(store.Secret{
			Name: name, Value: "old:" + name, Principal: "alice", Masked: true,
		}, now); err != nil {
			t.Fatalf("CreateOrReplaceSecret(%s): %v", name, err)
		}
	}

	boom := errors.New("cannot open")
	if _, err := s.RotateSecretValues(context.Background(), func(sec store.Secret) (string, error) {
		if sec.Name == "second" {
			return "", boom
		}
		return "new:" + sec.Name, nil
	}); !errors.Is(err, boom) {
		t.Fatalf("RotateSecretValues err=%v, want the reseal failure", err)
	}

	got, err := s.ListSecrets()
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	for _, sec := range got {
		if sec.Value != "old:"+sec.Name {
			t.Fatalf("secret %s value=%q after an abandoned rotation, want it untouched", sec.Name, sec.Value)
		}
	}
}

func TestRotateSecretValues_EmptyTable(t *testing.T) {
	s := storetest.Open(t)
	rotated, err := s.RotateSecretValues(context.Background(), func(store.Secret) (string, error) {
		t.Fatal("reseal called on an empty table")
		return "", nil
	})
	if err != nil {
		t.Fatalf("RotateSecretValues: %v", err)
	}
	if rotated != 0 {
		t.Fatalf("rotated=%d want 0", rotated)
	}
}
