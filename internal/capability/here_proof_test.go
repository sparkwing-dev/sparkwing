package capability

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestHereProofBindsTheRequestWithoutCarryingTheCredential(t *testing.T) {
	raw := "swk_ABCDEFGHIJKL" + strings.Repeat("s", 40)
	digest := sha256.Sum256([]byte(raw))
	stored := HereProofPublicKey(raw)
	if VerifyHereProof(HereProof(raw, "key-1", time.Unix(1_800_000_000, 0)), hex.EncodeToString(digest[:]), "key-1", time.Unix(1_800_000_000, 0)) {
		t.Fatal("bearer digest verifies here proof")
	}
	now := time.Unix(1_800_000_000, 0)
	proof := HereProof(raw, "key-1", now)
	if strings.Contains(proof, raw[16:]) || HereProofPrefix(proof) != raw[:16] {
		t.Fatalf("proof leaks the credential or loses its prefix: %s", proof)
	}
	if !VerifyHereProof(proof, stored, "key-1", now.Add(time.Minute)) {
		t.Fatal("a fresh proof failed")
	}
	for name, ok := range map[string]bool{
		"another request":    VerifyHereProof(proof, stored, "key-2", now),
		"expired":            VerifyHereProof(proof, stored, "key-1", now.Add(HereProofLifetime)),
		"another credential": VerifyHereProof(proof, strings.Repeat("0", 64), "key-1", now),
		"forged expiry":      VerifyHereProof(strings.Replace(proof, ".", ".9", 1), stored, "key-1", now),
	} {
		if ok {
			t.Fatalf("%s verified", name)
		}
	}
}

func TestStoredHereVerifierCannotSignMachineProof(t *testing.T) {
	raw := "swk_ABCDEFGHIJKL" + strings.Repeat("s", 40)
	now := time.Unix(1_800_000_000, 0)
	stored := HereProofPublicKey(raw)
	public, err := hex.DecodeString(stored)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(HereProof(raw, "request", now), ".")
	digest := sha256.Sum256([]byte(raw))
	for _, bytes := range [][]byte{public, digest[:]} {
		forged := ed25519.Sign(ed25519.NewKeyFromSeed(bytes), hereMessage(parts[0], "request", parts[1]))
		if VerifyHereProof(parts[0]+"."+parts[1]+"."+hex.EncodeToString(forged), stored, "request", now) {
			t.Fatal("stored verifier material minted a here proof")
		}
	}
}
