package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// FormatLogValue replaces Sparkwing credentials with stable, non-secret
// fingerprints. Stored 16-byte capability prefixes remain public identifiers.
func FormatLogValue(value string) string {
	// Most log fields contain no prefix. Avoid the regexp and allocations there.
	if !strings.Contains(value, "sw") {
		return value
	}
	return logCredential.ReplaceAllStringFunc(value, func(credential string) string {
		if len(credential) == 16 {
			return credential
		}
		digest := sha256.Sum256([]byte(credential))
		return "[credential:" + hex.EncodeToString(digest[:8]) + "]"
	})
}

var logCredential = regexp.MustCompile(`(?:` + strings.Join(CredentialPrefixes(), "|") + `)[A-Za-z0-9_-]+`)
