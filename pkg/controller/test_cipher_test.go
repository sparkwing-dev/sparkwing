package controller_test

import (
	"bytes"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: one key for every fixture, so a row seeded straight into the store
// opens on whichever fixture server reads it.
var testKey = bytes.Repeat([]byte{7}, secrets.KeySize)

func testCipher(t testing.TB) *secrets.Cipher {
	t.Helper()
	c, err := secrets.NewCipher(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sealedSecret(t testing.TB, team store.Team, sec store.Secret) store.Secret {
	t.Helper()
	v, err := testCipher(t).SealBound(string(team), sec.Name, sec.Pipeline, sec.Shared, sec.Masked, sec.Value)
	if err != nil {
		t.Fatal(err)
	}
	sec.Value = v
	return sec
}
