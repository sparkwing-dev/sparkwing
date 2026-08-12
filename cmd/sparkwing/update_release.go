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
	return releaseauth.PublicKeys()
}

// verifiedReleaseAsset is the byte boundary the installer consumes.
type verifiedReleaseAsset struct {
	name   string
	bytes  []byte
	digest string
}

// verifyReleaseAsset isolates the updater's existing checksum contract so the
// release-authentication boundary can be specified without filesystem writes.
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
	return verifiedReleaseAsset{name: assetName, bytes: asset, digest: digest}, nil
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
