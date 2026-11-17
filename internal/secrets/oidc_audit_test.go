package secrets

import (
	"bytes"
	"testing"
)

func TestOIDCAuditDigestRotationAndFieldSeparation(t *testing.T) {
	oldKey, newKey := bytes.Repeat([]byte{3}, KeySize), bytes.Repeat([]byte{4}, KeySize)
	old, err := NewCipher(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	id, digest, err := old.OIDCAuditDigest("", "audience", "person@example.test")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewCipherWithPrevious(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	matchedID, matched, err := rotated.OIDCAuditDigest(id, "audience", "person@example.test")
	if err != nil || matchedID != id || matched != digest {
		t.Fatalf("retired-key matching failed: %v", err)
	}
	for _, tc := range []struct{ keyID, field, value string }{
		{"", "audience", "person@example.test"},
		{id, "subject", "person@example.test"},
		{id, "audience", "other@example.test"},
	} {
		_, other, err := rotated.OIDCAuditDigest(tc.keyID, tc.field, tc.value)
		if err != nil || other == digest {
			t.Fatalf("digest domains collided: %v", err)
		}
	}
	current, err := NewCipher(newKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := current.OIDCAuditDigest(id, "audience", "person@example.test"); err == nil {
		t.Fatal("a missing retired key matched")
	}
	if _, _, err := old.OIDCAuditDigest("", "arbitrary", "person@example.test"); err == nil {
		t.Fatal("arbitrary digest domain accepted")
	}
}
