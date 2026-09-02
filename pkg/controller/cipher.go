package controller

import "github.com/sparkwing-dev/sparkwing/pkg/store"

// Cipher encrypts and decrypts secret values stored alongside runs.
// pkg/controller does not own the cipher implementation; it consumes
// one through this interface so external integrations can supply
// custom ciphers without depending on sparkwing's secrets package.
//
// The default implementation lives in internal/secrets and is used by
// cmd/sparkwing-controller. pkg/localws configures no cipher, so a
// laptop controller stores secret values as plaintext.
// External consumers building their own Server can pass nil to
// WithSecretsCipher (cipher-backed routes accept plaintext only) or
// supply any type whose method set matches this interface.
type Cipher interface {
	// Seal encrypts plain and returns a self-describing envelope
	// string suitable for round-tripping through Open. Implementations
	// must produce a different envelope per call (fresh nonce) even
	// for identical inputs.
	Seal(plain string) (string, error)
	// Open decrypts an envelope produced by Seal and returns the
	// original plaintext. Errors on truncated, tampered, or
	// wrong-key inputs.
	Open(envelope string) (string, error)
}

// BoundCipher is an optional extension of [Cipher] that binds a
// sealed value to the row fields that decide who may read the secret
// and how it is handled -- its name, its owning repository, whether an
// unscoped row answers every run, and whether it is redacted in run
// output -- so an envelope copied onto another row, or a row edited to
// widen its own access, no longer opens. A [Cipher] that also
// implements BoundCipher is used through these methods for every
// secret the controller seals and reads; one that does not keeps the
// unbound [Cipher.Seal] and [Cipher.Open] path.
//
// The implementation in internal/secrets satisfies it, and
// [github.com/sparkwing-dev/sparkwing/pkg/controller/ciphertest.TestBoundCipher]
// checks an implementation against this contract.
type BoundCipher interface {
	Cipher
	// SealBound encrypts plain with name, repo, shared and masked as
	// additional authenticated data; repo is empty for an unscoped
	// secret. Same nonce requirement as [Cipher.Seal].
	SealBound(name, repo string, shared, masked bool, plain string) (string, error)
	// OpenBound decrypts an envelope sealed for this combination of
	// name, repo, shared and masked, and errors when the envelope
	// was sealed for a different one. Implementations that also hold
	// envelopes written before binding open those unchanged.
	OpenBound(name, repo string, shared, masked bool, envelope string) (string, error)
}

type secretBinding struct {
	Name   string
	Repo   string
	Shared bool
	Masked bool
}

func bindingForRow(sec *store.Secret) secretBinding {
	return secretBinding{Name: sec.Name, Repo: sec.Repo, Shared: sec.Shared, Masked: sec.Masked}
}

func sealSecret(c Cipher, b secretBinding, plain string) (string, error) {
	if bc, ok := c.(BoundCipher); ok {
		return bc.SealBound(b.Name, b.Repo, b.Shared, b.Masked, plain)
	}
	return c.Seal(plain)
}

func openSecret(c Cipher, b secretBinding, envelope string) (string, error) {
	if bc, ok := c.(BoundCipher); ok {
		return bc.OpenBound(b.Name, b.Repo, b.Shared, b.Masked, envelope)
	}
	return c.Open(envelope)
}
