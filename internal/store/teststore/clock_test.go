package teststore_test

import (
	"crypto/sha256"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestDefaultFixtureClockDoesNotConsumeAuthorityDuringSetup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		s, err := teststore.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := teststore.ProvisionCredential(t.Context(), s, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = teststore.OpenExisting(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
		opened := teststore.Now(s)
		time.Sleep(24 * time.Hour)
		snapshot, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{
			Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte(raw)),
		}, capability.RequestEvidence{})
		if err != nil {
			t.Fatal(err)
		}
		if !snapshot.Now.Equal(opened) {
			t.Fatalf("fixture clock advanced during setup: %s, want %s", snapshot.Now, opened)
		}
	})
}
