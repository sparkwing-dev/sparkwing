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
