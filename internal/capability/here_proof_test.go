package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestHereProofBindsTheRequestWithoutCarryingTheCredential(t *testing.T) {
	raw := "swk_ABCDEFGHIJKL" + strings.Repeat("s", 40)
	digest := sha256.Sum256([]byte(raw))
	stored := hex.EncodeToString(digest[:])
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
