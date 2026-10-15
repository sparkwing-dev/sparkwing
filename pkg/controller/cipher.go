package controller

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Cipher supplies envelope encryption for controller integrations.
type Cipher interface {
	// Seal encrypts plaintext with a fresh nonce.
	Seal(plain string) (string, error)
	// Open decrypts an envelope and refuses invalid bytes or keys.
	Open(envelope string) (string, error)
}

// BoundCipher authenticates a secret envelope against its tenant and row access fields.
type BoundCipher interface {
	Cipher
	// SealBound encrypts plaintext with a fresh nonce and the complete row binding.
	SealBound(team, name, scope string, shared, masked bool, plain string) (string, error)
	// OpenBound decrypts only an envelope sealed for the complete row binding.
	OpenBound(team, name, scope string, shared, masked bool, envelope string) (string, error)
}

type secretBinding struct {
	Team   store.Team
	Name   string
	Scope  string
	Shared bool
	Masked bool
}

func bindingForRow(team store.Team, sec *store.Secret) secretBinding {
	return secretBinding{Team: team, Name: sec.Name, Scope: sec.Pipeline, Shared: sec.Shared, Masked: sec.Masked}
}

// SealSecretValue encrypts a secret using its complete tenant and row binding.
func SealSecretValue(c Cipher, team store.Team, sec *store.Secret, plain string) (string, error) {
	return sealSecret(c, bindingForRow(team, sec), plain)
}

// OpenSecretValue decrypts a secret using its complete tenant and row binding.
func OpenSecretValue(c Cipher, team store.Team, sec *store.Secret) (string, error) {
	return openSecret(c, bindingForRow(team, sec), sec.Value)
}

var errBoundCipherRequired = errors.New("row-bound encryption is required")

func sealSecret(c Cipher, b secretBinding, plain string) (string, error) {
	bc, ok := c.(BoundCipher)
	if !ok {
		return "", errBoundCipherRequired
	}
	return bc.SealBound(string(b.Team), b.Name, b.Scope, b.Shared, b.Masked, plain)
}

func openSecret(c Cipher, b secretBinding, envelope string) (string, error) {
	bc, ok := c.(BoundCipher)
	if !ok {
		return "", errBoundCipherRequired
	}
	return bc.OpenBound(string(b.Team), b.Name, b.Scope, b.Shared, b.Masked, envelope)
}
