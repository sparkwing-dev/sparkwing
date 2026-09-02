package controller

// Cipher encrypts and decrypts secret values stored alongside runs.
// pkg/controller does not own the cipher implementation; it consumes
// one through this interface so external integrations can supply
// custom ciphers without depending on sparkwing's secrets package.
//
// The default implementation lives in internal/secrets and is used by
// cmd/sparkwing-controller (cluster) and pkg/localws (laptop).
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
// sealed value to the secret it belongs to -- its name and its owning
// repository -- so an envelope copied onto another row no longer
// opens. A [Cipher] that also implements BoundCipher is used through
// these methods for every secret the controller seals and reads; one
// that does not keeps the unbound [Cipher.Seal] and [Cipher.Open]
// path.
//
// The implementation in internal/secrets satisfies it.
type BoundCipher interface {
	Cipher
	// SealBound encrypts plain with name and repo as additional
	// authenticated data; repo is empty for an unscoped secret.
	// Same nonce requirement as [Cipher.Seal].
	SealBound(name, repo, plain string) (string, error)
	// OpenBound decrypts an envelope sealed for name and repo, and
	// errors when the envelope was sealed for a different pair.
	// Implementations that also hold envelopes written before
	// binding open those unchanged.
	OpenBound(name, repo, envelope string) (string, error)
}

func sealSecret(c Cipher, name, repo, plain string) (string, error) {
	if bc, ok := c.(BoundCipher); ok {
		return bc.SealBound(name, repo, plain)
	}
	return c.Seal(plain)
}

func openSecret(c Cipher, name, repo, envelope string) (string, error) {
	if bc, ok := c.(BoundCipher); ok {
		return bc.OpenBound(name, repo, envelope)
	}
	return c.Open(envelope)
}
