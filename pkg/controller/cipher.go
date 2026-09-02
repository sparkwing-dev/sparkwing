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

// NamedCipher is an optional extension of [Cipher] that binds a
// sealed value to the name of the secret it belongs to, so an
// envelope copied onto another name no longer opens. A [Cipher] that
// also implements NamedCipher is used through these methods for every
// secret the controller seals and reads; one that does not keeps the
// unbound [Cipher.Seal] and [Cipher.Open] path.
//
// The implementation in internal/secrets satisfies it.
type NamedCipher interface {
	Cipher
	// SealNamed encrypts plain with name as additional
	// authenticated data. Same nonce requirement as [Cipher.Seal].
	SealNamed(name, plain string) (string, error)
	// OpenNamed decrypts an envelope sealed under name, and errors
	// when the envelope was sealed under a different one.
	// Implementations that also hold envelopes written before name
	// binding open those unchanged.
	OpenNamed(name, envelope string) (string, error)
}

func sealSecret(c Cipher, name, plain string) (string, error) {
	if nc, ok := c.(NamedCipher); ok {
		return nc.SealNamed(name, plain)
	}
	return c.Seal(plain)
}

func openSecret(c Cipher, name, envelope string) (string, error) {
	if nc, ok := c.(NamedCipher); ok {
		return nc.OpenNamed(name, envelope)
	}
	return c.Open(envelope)
}
