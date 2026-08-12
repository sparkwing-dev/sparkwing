package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const releasePublicKeyBase64 = "SCA8nBcnHkYcyP6g+Quuwy5UR4bKJLlwrf7FcWZsXOI="

func releasePublicKey() (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(releasePublicKeyBase64)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("embedded release public key is invalid")
	}
	return ed25519.PublicKey(decoded), nil
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
