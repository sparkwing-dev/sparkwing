package capability

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// HereProofLifetime bounds how long a here proof stays acceptable after it is made.
const HereProofLifetime = 5 * time.Minute

// HereProof proves possession of a runner credential for one intake request without sending the credential: an
// HMAC keyed by the credential's stored digest over its prefix, the request's idempotency key and an expiry.
func HereProof(raw, idempotencyKey string, now time.Time) string {
	if len(raw) < 16 || idempotencyKey == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(raw))
	expires := strconv.FormatInt(now.Add(HereProofLifetime).Unix(), 10)
	return raw[:16] + "." + expires + "." + hereMAC(digest[:], raw[:16], idempotencyKey, expires)
}

// HereProofPrefix returns the credential prefix a proof names, or "" for a malformed proof.
func HereProofPrefix(proof string) string {
	prefix, _, ok := strings.Cut(proof, ".")
	if !ok || len(prefix) != 16 {
		return ""
	}
	return prefix
}

// VerifyHereProof checks a proof against the credential's stored hex digest, the request's key and the time.
func VerifyHereProof(proof, digestHex, idempotencyKey string, now time.Time) bool {
	parts := strings.Split(proof, ".")
	digest, err := hex.DecodeString(digestHex)
	if len(parts) != 3 || err != nil || idempotencyKey == "" || len(parts[0]) != 16 {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || !now.Before(time.Unix(expires, 0)) || time.Unix(expires, 0).After(now.Add(HereProofLifetime)) {
		return false
	}
	return hmac.Equal([]byte(parts[2]), []byte(hereMAC(digest, parts[0], idempotencyKey, parts[1])))
}

func hereMAC(key []byte, prefix, idempotencyKey, expires string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("sparkwing-here-proof\x00" + prefix + "\x00" + idempotencyKey + "\x00" + expires))
	return hex.EncodeToString(mac.Sum(nil))
}
