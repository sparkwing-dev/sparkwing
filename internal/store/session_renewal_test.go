package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestSessionRenewalKeepsLaterExpiryAndRefusesDeletedRow(t *testing.T) {
	s, account, now := sessionRuntimeStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.csrfCipher, s.csrfKey = cipher, nil
	created := *now
	raw, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	*now = created.Add(2 * 24 * time.Hour)
	newer, err := s.AsOperator().LookupSessionAndRenew(t.Context(), raw, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	*now = created.Add(24 * time.Hour)
	older, err := s.AsOperator().LookupSessionAndRenew(t.Context(), raw, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !older.ExpiresAt.Equal(newer.ExpiresAt) || !older.LastUsedAt.Equal(*newer.LastUsedAt) {
		t.Fatalf("out-of-order use moved session: %+v -> %+v", newer, older)
	}
	if err := s.AsOperator().DeleteSession(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().LookupSessionAndRenew(t.Context(), raw, 7*24*time.Hour, 30*24*time.Hour); err == nil {
		t.Fatal("deleted session authenticated")
	}
}
