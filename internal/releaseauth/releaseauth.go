// Package releaseauth signs and verifies immutable Sparkwing release assets.
package releaseauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

// TrustedPublicKeys is the updater trust set. Rotation adds the replacement
// key before the release signer changes and removes the retired key later.
var TrustedPublicKeys = []string{
	"SCA8nBcnHkYcyP6g+Quuwy5UR4bKJLlwrf7FcWZsXOI=",
}

func TrustedPublicKey(encoded string) bool {
	for _, trusted := range TrustedPublicKeys {
		if encoded == trusted {
			return true
		}
	}
	return false
}

func PublicKeys() ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(TrustedPublicKeys))
	for _, encoded := range TrustedPublicKeys {
		key, err := PublicKey(encoded)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func PrivateKey(encoded string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode release signing seed: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("release signing seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func PublicKey(encoded string) (ed25519.PublicKey, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode release public key: %w", err)
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("release public key is %d bytes, want %d", len(key), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(key), nil
}

func Sign(privateKey ed25519.PrivateKey, body []byte) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("release private key is invalid")
	}
	return ed25519.Sign(privateKey, body), nil
}

func Verify(publicKey ed25519.PublicKey, body, signature []byte) error {
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, body, signature) {
		return errors.New("release signature is invalid")
	}
	return nil
}
