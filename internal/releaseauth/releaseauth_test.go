package releaseauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestPrivateKeyAcceptsSeedAndExpandedPrivateKey(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	want := ed25519.NewKeyFromSeed(seed)

	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{name: "seed", raw: seed},
		{name: "expanded private key", raw: want},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PrivateKey(base64.StdEncoding.EncodeToString(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatal("private key differs from seed authority")
			}
		})
	}
}

func TestPrivateKeyRejectsUnsupportedLength(t *testing.T) {
	_, err := PrivateKey(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize+1)))
	if err == nil {
		t.Fatal("unsupported key length accepted")
	}
}

func TestPrivateKeyRejectsInconsistentExpandedKey(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	key := append([]byte(nil), ed25519.NewKeyFromSeed(seed)...)
	key[ed25519.SeedSize] ^= 1
	_, err := PrivateKey(base64.StdEncoding.EncodeToString(key))
	if err == nil {
		t.Fatal("expanded key with mismatched public half accepted")
	}
}
