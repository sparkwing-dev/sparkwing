package sparkwing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Key composes a CacheKey from arbitrary parts. Parts are stringified
// (via fmt.Sprintf("%v")), joined with a separator unlikely to appear
// in values, and SHA-256 hashed. The result is a 16-char hex digest
// prefixed with "ck:" so cache rows are recognizable in logs.
//
//	sparkwing.Key("deploy", target, build.Output().Digest)
//
// Determinism caveats:
//   - nil values hash to their Go zero stringification ("<nil>"); pass a
//     sentinel string if distinction matters.
//   - slices and maps stringify through Sprintf's default format, which
//     is order-sensitive for maps. Avoid using raw maps as parts; pass
//     a sorted slice of key=value strings instead.
//   - Refs serialize to their NodeID by default. If you want the
//     upstream's *output* in the key, resolve the Ref first:
//     `sparkwing.Key(..., ref.Get(ctx).Digest)`.
func Key(parts ...any) CacheKey {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('\x1e') // ASCII record-separator
		}
		fmt.Fprintf(&b, "%v", p)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return CacheKey("ck:" + hex.EncodeToString(sum[:])[:16])
}
