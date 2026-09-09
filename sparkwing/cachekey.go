package sparkwing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// NoCache bypasses memoization when a [CacheKeyFn] returns it with a nil error.
// An empty key fails resolution.
const NoCache CacheKey = "ck:nocache"

// IsNoCache reports whether k is the explicit [NoCache] sentinel.
func (k CacheKey) IsNoCache() bool { return k == NoCache }

// Key hashes the parts' fmt %v representations, separated by byte 0x1e,
// into a "ck:" prefix and 16 hexadecimal characters.
// Choose parts with stable, distinct representations: formatting omits
// type information, and a part containing 0x1e can alias multiple parts.
// Resolve a [Ref] before passing its output into the key; a Ref itself
// formats as its node ID.
func Key(parts ...any) CacheKey {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('\x1e')
		}
		fmt.Fprintf(&b, "%v", p)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return CacheKey("ck:" + hex.EncodeToString(sum[:])[:16])
}
