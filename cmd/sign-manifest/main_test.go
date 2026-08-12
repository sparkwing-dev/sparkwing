package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// TestSignFileRoundTrips proves the detached signature this tool writes
// verifies against the corresponding public key with the same crypto the
// updater uses -- signer and verifier agree byte-for-byte.
func TestSignFileRoundTrips(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "SHA256SUMS")
	out := filepath.Join(dir, "SHA256SUMS.sig")
	manifest := []byte("deadbeef  sparkwing-linux-amd64\ncafef00d  sparkwing-darwin-arm64\n")
	if err := os.WriteFile(in, manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := signFile(priv, in, out); err != nil {
		t.Fatalf("signFile: %v", err)
	}
	sig, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, manifest, sig) {
		t.Fatal("signature did not verify against the public key")
	}
	// Tamper with the manifest: the same signature must not verify.
	if ed25519.Verify(pub, append(manifest, 'x'), sig) {
		t.Fatal("signature verified over tampered manifest")
	}
}

// TestLoadSigningKey accepts both a full 64-byte private key and a bare
// 32-byte seed, and rejects garbage.
func TestLoadSigningKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	full := base64.StdEncoding.EncodeToString(priv)
	got, err := loadSigningKey(full)
	if err != nil {
		t.Fatalf("full key: %v", err)
	}
	if !got.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatal("full key: recovered public key mismatch")
	}

	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	got, err = loadSigningKey(seed)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !got.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatal("seed: recovered public key mismatch")
	}

	if _, err := loadSigningKey(""); err == nil {
		t.Fatal("empty key was accepted")
	}
	if _, err := loadSigningKey("not-base64!!!"); err == nil {
		t.Fatal("non-base64 key was accepted")
	}
	if _, err := loadSigningKey(base64.StdEncoding.EncodeToString([]byte("too short"))); err == nil {
		t.Fatal("wrong-length key was accepted")
	}
}
