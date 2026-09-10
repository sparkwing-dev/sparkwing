package releaseasset

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyBindsAssetAndManifestToOneTrustedKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Binary: SparkwingRunner, GOOS: "windows", GOARCH: "arm64"}
	name, err := target.Name()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("runner")
	digest := sha256.Sum256(body)
	manifest := []byte(hex.EncodeToString(digest[:]) + "  " + name + "\n")
	verified, err := Verify([]ed25519.PublicKey{publicKey}, manifest, ed25519.Sign(privateKey, manifest), target, body, ed25519.Sign(privateKey, body))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Name() != name || verified.Digest() != hex.EncodeToString(digest[:]) {
		t.Fatalf("Verify() = %+v", verified)
	}

	otherPublic, otherPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify([]ed25519.PublicKey{publicKey, otherPublic}, manifest, ed25519.Sign(privateKey, manifest), target, body, ed25519.Sign(otherPrivate, body))
	if err == nil {
		t.Fatal("manifest and asset signatures from different trusted keys were accepted")
	}
}
