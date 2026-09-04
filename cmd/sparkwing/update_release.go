package main

import (
	"crypto/ed25519"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
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
	target, err := releaseasset.ParseName(assetName)
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	verified, err := releaseasset.Verify([]ed25519.PublicKey{publicKey}, manifest, manifestSig, target, asset, assetSig)
	return fromSharedVerified(verified), err
}

func manifestSignedByTrustSet(publicKeys []ed25519.PublicKey, manifest, manifestSig []byte) bool {
	return releaseasset.ManifestSignedBy(publicKeys, manifest, manifestSig)
}

func verifyReleaseAssetWithTrustSet(publicKeys []ed25519.PublicKey, manifest, manifestSig []byte, assetName string, asset, assetSig []byte) (verifiedReleaseAsset, error) {
	target, err := releaseasset.ParseName(assetName)
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	verified, err := releaseasset.Verify(publicKeys, manifest, manifestSig, target, asset, assetSig)
	return fromSharedVerified(verified), err
}

func manifestDigest(manifest []byte, assetName string) (string, error) {
	return releaseasset.ManifestDigest(manifest, assetName)
}

func fromSharedVerified(asset releaseasset.Verified) verifiedReleaseAsset {
	return verifiedReleaseAsset{
		name: asset.Name(), bytes: asset.Bytes(), digest: asset.Digest(),
		manifest: asset.Manifest(), manifestSig: asset.ManifestSignature(),
	}
}
