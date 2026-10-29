package controller_test

import (
	"bytes"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
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
