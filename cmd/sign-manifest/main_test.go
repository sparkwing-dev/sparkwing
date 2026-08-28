package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

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

	if ed25519.Verify(pub, append(manifest, 'x'), sig) {
		t.Fatal("signature verified over tampered manifest")
	}
}

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

func TestVerifyFile_MatchAndMismatch(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "SHA256SUMS")
	sig := filepath.Join(dir, "SHA256SUMS.sig")
	if err := os.WriteFile(in, []byte("deadbeef  sparkwing-linux-amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := signFile(priv, in, sig); err != nil {
		t.Fatal(err)
	}

	if err := verifyFile(hex.EncodeToString(pub), in, sig); err != nil {
		t.Fatalf("matching key failed to verify: %v", err)
	}

	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := verifyFile(hex.EncodeToString(otherPub), in, sig); err == nil {
		t.Fatal("a mismatched public key verified; the CI guard would not catch a keypair mismatch")
	}

	zero := hex.EncodeToString(make([]byte, ed25519.PublicKeySize))
	if err := verifyFile(zero, in, sig); err == nil {
		t.Fatal("placeholder key verified; unarmed build not rejected")
	}
}
