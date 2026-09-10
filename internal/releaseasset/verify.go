package releaseasset

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Verified is an asset whose bytes match both trusted signatures and the
// signed checksum manifest. Callers cannot construct this capability.
type Verified struct {
	name              string
	bytes             []byte
	digest            string
	manifest          []byte
	manifestSignature []byte
}

// Name returns the authenticated release asset name.
func (asset Verified) Name() string { return asset.name }

// Bytes returns a copy of the authenticated executable bytes.
func (asset Verified) Bytes() []byte { return bytes.Clone(asset.bytes) }

// Digest returns the authenticated lowercase SHA-256 digest.
func (asset Verified) Digest() string { return asset.digest }

// Manifest returns a copy of the signed release manifest.
func (asset Verified) Manifest() []byte { return bytes.Clone(asset.manifest) }

// ManifestSignature returns a copy of the trusted manifest signature.
func (asset Verified) ManifestSignature() []byte { return bytes.Clone(asset.manifestSignature) }

// Verify requires one trusted key to sign both the manifest and asset bytes.
func Verify(publicKeys []ed25519.PublicKey, manifest, manifestSignature []byte, target Target, asset, assetSignature []byte) (Verified, error) {
	name, err := target.Name()
	if err != nil {
		return Verified{}, err
	}
	for _, publicKey := range publicKeys {
		verified, verifyErr := verifyWithKey(publicKey, manifest, manifestSignature, name, asset, assetSignature)
		if verifyErr == nil {
			return verified, nil
		}
	}
	return Verified{}, errors.New("release signatures do not match the updater trust set")
}

func verifyWithKey(publicKey ed25519.PublicKey, manifest, manifestSignature []byte, name string, asset, assetSignature []byte) (Verified, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return Verified{}, errors.New("release public key is invalid")
	}
	if !ed25519.Verify(publicKey, manifest, manifestSignature) {
		return Verified{}, errors.New("SHA256SUMS signature is invalid")
	}
	if !ed25519.Verify(publicKey, asset, assetSignature) {
		return Verified{}, fmt.Errorf("signature is invalid for %s", name)
	}
	digest, err := ManifestDigest(manifest, name)
	if err != nil {
		return Verified{}, err
	}
	actual := sha256.Sum256(asset)
	actualHex := hex.EncodeToString(actual[:])
	if digest != actualHex {
		return Verified{}, fmt.Errorf("checksum mismatch for %s", name)
	}
	return Verified{
		name:              name,
		bytes:             bytes.Clone(asset),
		digest:            digest,
		manifest:          bytes.Clone(manifest),
		manifestSignature: bytes.Clone(manifestSignature),
	}, nil
}

// ManifestSignedBy reports whether a manifest signature matches the trust set.
func ManifestSignedBy(publicKeys []ed25519.PublicKey, manifest, signature []byte) bool {
	for _, key := range publicKeys {
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, manifest, signature) {
			return true
		}
	}
	return false
}

// ManifestDigest returns one unambiguous lowercase SHA-256 digest.
func ManifestDigest(manifest []byte, assetName string) (string, error) {
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
