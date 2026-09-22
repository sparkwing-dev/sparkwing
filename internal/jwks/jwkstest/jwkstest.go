// Package jwkstest signs RS256 tokens with a generated key and serves the key
// set that verifies them, so a suite can stand in for an identity provider.
package jwkstest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"testing"
)

// Signer holds a signing key and the key id tokens name.
type Signer struct {
	Key *rsa.PrivateKey
	Kid string
}

// NewSigner generates a 2048-bit key.
func NewSigner(t testing.TB, kid string) *Signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &Signer{Key: key, Kid: kid}
}

// Sign returns claims as a compact RS256 JWT under s's key id.
func (s *Signer) Sign(claims map[string]any) (string, error) {
	return s.SignWith(s.Key, claims)
}

// SignWith signs under s's key id with another key, which is how a test
// builds a token the published key set does not verify.
func (s *Signer) SignWith(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	head, err := json.Marshal(map[string]string{"alg": "RS256", "kid": s.Kid, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(head) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ServeKeys answers with the key set that verifies s's tokens.
func (s *Signer) ServeKeys(w http.ResponseWriter, _ *http.Request) {
	pub := s.Key.PublicKey
	body, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": s.Kid, "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if _, err := w.Write(body); err != nil {
		return
	}
}
