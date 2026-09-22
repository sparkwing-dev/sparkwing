// Package licensetest signs licenses with a key pair a test generates, so a
// suite can exercise a licensed controller without any private key living in
// this repository.
package licensetest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// Terms is what a signed license grants.
type Terms struct {
	Features  []string
	IssuedTo  string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// NewKey returns a fresh key pair.
func NewKey(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// Sign returns the license text for terms, signed by priv.
func Sign(t testing.TB, priv ed25519.PrivateKey, terms Terms) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"features":   terms.Features,
		"issued_to":  terms.IssuedTo,
		"issued_at":  terms.IssuedAt.UTC(),
		"expires_at": terms.ExpiresAt.UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return Encode(body, ed25519.Sign(priv, body))
}

// Encode joins a payload and a signature into license text without checking
// that one signs the other, which is how a test builds a tampered license.
func Encode(payload, sig []byte) string {
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}
