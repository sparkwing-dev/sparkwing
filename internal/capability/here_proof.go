package capability

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// hereProofLifetime bounds how long a here proof stays acceptable after it is made.
const hereProofLifetime = 5 * time.Minute

// HereProof signs one intake request with a key derived from the runner credential.
// The stored public verifier cannot mint proofs.
func HereProof(raw, idempotencyKey string, now time.Time) string {
	if len(raw) < CredentialPrefixLength || idempotencyKey == "" {
		return ""
	}
	key := hereProofPrivateKey(raw)
	expires := strconv.FormatInt(now.Add(hereProofLifetime).Unix(), 10)
	return raw[:CredentialPrefixLength] + "." + expires + "." + hex.EncodeToString(ed25519.Sign(key, hereMessage(raw[:CredentialPrefixLength], idempotencyKey, expires)))
}

// HereProofPrefix returns the credential prefix a proof names, or "" for a malformed proof.
func HereProofPrefix(proof string) string {
	prefix, _, ok := strings.Cut(proof, ".")
	if !ok || len(prefix) != CredentialPrefixLength {
		return ""
	}
	return prefix
}

// HereProofPublicKey derives the public verifier stored with a runner credential.
func HereProofPublicKey(raw string) string {
	key := hereProofPrivateKey(raw)
	return hex.EncodeToString(key.Public().(ed25519.PublicKey))
}

func hereProofPrivateKey(raw string) ed25519.PrivateKey {
	seed, err := hkdf.Key(sha256.New, []byte(raw), nil, "sparkwing-here-proof-key", ed25519.SeedSize)
	if err != nil {
		panic(err)
	}
	return ed25519.NewKeyFromSeed(seed)
}

// VerifyHereProof checks a proof against its stored verifier, the request's key and the time.
func VerifyHereProof(proof, publicKeyHex, idempotencyKey string, now time.Time) bool {
	parts := strings.Split(proof, ".")
	key, err := hex.DecodeString(publicKeyHex)
	if len(parts) != 3 || err != nil || len(key) != ed25519.PublicKeySize || idempotencyKey == "" || len(parts[0]) != CredentialPrefixLength {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || !now.Before(time.Unix(expires, 0)) || time.Unix(expires, 0).After(now.Add(hereProofLifetime)) {
		return false
	}
	signature, err := hex.DecodeString(parts[2])
	return err == nil && ed25519.Verify(ed25519.PublicKey(key), hereMessage(parts[0], idempotencyKey, parts[1]), signature)
}

func hereMessage(prefix, idempotencyKey, expires string) []byte {
	return []byte("sparkwing-here-proof\x00" + prefix + "\x00" + idempotencyKey + "\x00" + expires)
}
