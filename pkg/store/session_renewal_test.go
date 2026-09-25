package store

import (
	"testing"
	"time"
)

func TestSessionRenewalKeepsLaterExpiryAndRefusesDeletedRow(t *testing.T) {
	s := newTestStore(t)
	created := time.Now().UTC().Truncate(time.Second)
	raw, _, _, err := s.CreateSession("alice", []string{"runs.read"}, 7*24*time.Hour, created)
	if err != nil {
		t.Fatal(err)
	}

	newer, err := s.LookupSessionAndRenew(raw, created.Add(2*24*time.Hour), 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	older, err := s.LookupSessionAndRenew(raw, created.Add(24*time.Hour), 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !older.ExpiresAt.Equal(newer.ExpiresAt) {
		t.Errorf("out-of-order use moved expiry from %s to %s", newer.ExpiresAt, older.ExpiresAt)
	}
	if err := s.DeleteSession(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSessionAndRenew(raw, created.Add(3*24*time.Hour), 7*24*time.Hour, 30*24*time.Hour); err == nil {
		t.Error("revoked session authenticated")
	}
}
