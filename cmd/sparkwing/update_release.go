package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func releasePublicKeys() ([]ed25519.PublicKey, error) {
	if len(updateVerifyKey) != 0 {
		if isPlaceholderUpdateKey(updateVerifyKey) {
			return nil, errors.New("release verification key is not armed")
		}
		return []ed25519.PublicKey{updateVerifyKey}, nil
	}
	return releaseauth.PublicKeys()
}

func isPlaceholderUpdateKey(key ed25519.PublicKey) bool {
	if len(key) != ed25519.PublicKeySize {
		return true
	}
	for _, b := range key {
		if b != 0 {
			return false
		}
	}
	return true
}

type verifiedReleaseAsset struct {
	name   string
	bytes  []byte
	digest string

	manifest    []byte
	manifestSig []byte
}

func verifyReleaseAsset(publicKey ed25519.PublicKey, manifest, manifestSig []byte, assetName string, asset, assetSig []byte) (verifiedReleaseAsset, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return verifiedReleaseAsset{}, errors.New("release public key is invalid")
	}
	if !ed25519.Verify(publicKey, manifest, manifestSig) {
		return verifiedReleaseAsset{}, errors.New("SHA256SUMS signature is invalid")
	}
	if !ed25519.Verify(publicKey, asset, assetSig) {
		return verifiedReleaseAsset{}, fmt.Errorf("signature is invalid for %s", assetName)
	}
	digest, err := manifestDigest(manifest, assetName)
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	actual := sha256.Sum256(asset)
	actualHex := hex.EncodeToString(actual[:])
	if digest != actualHex {
		return verifiedReleaseAsset{}, fmt.Errorf("checksum mismatch for %s", assetName)
	}
	return verifiedReleaseAsset{
		name:        assetName,
		bytes:       asset,
		digest:      digest,
		manifest:    manifest,
		manifestSig: manifestSig,
	}, nil
}

// manifestSignedByTrustSet reports whether any release key signed this manifest.
// It is the offline half of the release check: no asset signature, no network.
func manifestSignedByTrustSet(publicKeys []ed25519.PublicKey, manifest, manifestSig []byte) bool {
	for _, key := range publicKeys {
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, manifest, manifestSig) {
			return true
		}
	}
	return false
}

func verifyReleaseAssetWithTrustSet(publicKeys []ed25519.PublicKey, manifest, manifestSig []byte, assetName string, asset, assetSig []byte) (verifiedReleaseAsset, error) {
	for _, publicKey := range publicKeys {
		verified, err := verifyReleaseAsset(publicKey, manifest, manifestSig, assetName, asset, assetSig)
		if err == nil {
			return verified, nil
		}
	}
	return verifiedReleaseAsset{}, errors.New("release signatures do not match the updater trust set")
}

func manifestDigest(manifest []byte, assetName string) (string, error) {
	var digest string
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != assetName {
			continue
		}
		if digest != "" {
			return "", fmt.Errorf("duplicate %s entry in SHA256SUMS", assetName)
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", fmt.Errorf("malformed SHA-256 digest for %s", assetName)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", fmt.Errorf("malformed SHA-256 digest for %s: %w", assetName, err)
		}
		digest = strings.ToLower(fields[0])
	}
	if digest == "" {
		return "", errors.New(assetName + " not listed in SHA256SUMS")
	}
	return digest, nil
}
