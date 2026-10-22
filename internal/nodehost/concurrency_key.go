package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ScopeLabelFromKey renders the scope and complete host identity of a concurrency key.
func ScopeLabelFromKey(key string) string {
	_, scope, identity, _, ok := runspec.ParseConcurrencyKey(key)
	if !ok {
		return "unknown"
	}
	switch scope {
	case "global":
		return "global"
	default:
		return scope + " (" + identity + ")"
	}
}
