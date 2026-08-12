package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyReleaseAssetRequiresManifestAndAssetSignatures(t *testing.T) {
	t.Parallel()

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	assetName := "sparkwing-darwin-arm64"
	asset := []byte("signed release asset")
	digest := sha256.Sum256(asset)
	manifest := []byte(hex.EncodeToString(digest[:]) + "  " + assetName + "\n")
	manifestSig := ed25519.Sign(privateKey, manifest)
	assetSig := ed25519.Sign(privateKey, asset)

	tests := []struct {
		name        string
		manifestSig []byte
		assetSig    []byte
		wantErr     bool
	}{
		{name: "valid", manifestSig: manifestSig, assetSig: assetSig},
		{name: "missing manifest signature", assetSig: assetSig, wantErr: true},
		{name: "bad manifest signature", manifestSig: append([]byte(nil), assetSig...), assetSig: assetSig, wantErr: true},
		{name: "missing asset signature", manifestSig: manifestSig, wantErr: true},
		{name: "bad asset signature", manifestSig: manifestSig, assetSig: append([]byte(nil), manifestSig...), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := verifyReleaseAsset(publicKey, manifest, test.manifestSig, assetName, asset, test.assetSig)
			if (err != nil) != test.wantErr {
				t.Fatalf("verifyReleaseAsset() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestVerifyReleaseAssetRejectsAmbiguousOrMismatchedManifest(t *testing.T) {
	t.Parallel()

	seed := make([]byte, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	assetName := "sparkwing-linux-amd64"
	asset := []byte("asset")
	digest := sha256.Sum256(asset)
	line := hex.EncodeToString(digest[:]) + "  " + assetName + "\n"

	tests := []struct {
		name     string
		manifest []byte
	}{
		{name: "duplicate", manifest: []byte(line + line)},
		{name: "malformed digest", manifest: []byte("abc  " + assetName + "\n")},
		{name: "digest mismatch", manifest: []byte(hex.EncodeToString(make([]byte, sha256.Size)) + "  " + assetName + "\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifestSig := ed25519.Sign(privateKey, test.manifest)
			assetSig := ed25519.Sign(privateKey, asset)
			if _, err := verifyReleaseAsset(publicKey, test.manifest, manifestSig, assetName, asset, assetSig); err == nil {
				t.Fatal("verifyReleaseAsset() succeeded")
			}
		})
	}
}
