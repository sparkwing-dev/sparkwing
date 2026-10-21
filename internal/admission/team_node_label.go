package admission

import (
	"crypto/sha256"
	"encoding/hex"
)

// TeamNodeValue is the protected node label value for one team's job namespace.
func TeamNodeValue(namespace, team string) string {
	return TeamNodeHash(namespace + "/" + team)
}

// TeamNodeHash produces a Kubernetes label value for an opaque team key.
func TeamNodeHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256-" + hex.EncodeToString(sum[:20])
}
