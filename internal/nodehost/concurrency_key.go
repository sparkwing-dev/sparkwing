package nodehost

import (
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ConcurrencyKey scopes a validated group using host-owned run and machine identity.
func ConcurrencyKey(c nodeproto.ConcurrencySpec, runID, boxID string) string {
	return scopedConcurrencyKey(c.Group, value(c.Scope), runID, boxID)
}

func scopedConcurrencyKey(name, scope, runID, boxID string) string {
	identity := ""
	switch scope {
	case "run":
		identity = runID
	case "box":
		identity = boxID
	default:
		return runspec.ConcurrencyKey("job", "global", "", name)
	}
	return runspec.ConcurrencyKey("job", scope, identity, name)
}

const memoKeyPrefix = "memo:"

// ScopeLabelFromKey renders the scope and complete host identity of a concurrency key.
func ScopeLabelFromKey(key string) string {
	if strings.HasPrefix(key, memoKeyPrefix) {
		return "content-cache"
	}
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
