package nodehost

import (
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
		return "global:" + name
	}
	return scope + ":" + strconv.Itoa(len(identity)) + ":" + identity + ":" + name
}

// ParseConcurrencyKey recovers the scope, host identity and group from a host key.
func ParseConcurrencyKey(key string) (scope, identity, group string, ok bool) {
	scope, rest, found := strings.Cut(key, ":")
	if !found {
		return "", "", "", false
	}
	if scope == "global" {
		return scope, "", rest, true
	}
	if scope != "run" && scope != "box" {
		return "", "", "", false
	}
	size, rest, found := strings.Cut(rest, ":")
	n, err := strconv.Atoi(size)
	if !found || err != nil || n < 0 || n >= len(rest) || strconv.Itoa(n) != size || rest[n] != ':' {
		return "", "", "", false
	}
	return scope, rest[:n], rest[n+1:], true
}

const memoKeyPrefix = "memo:"

// ScopeLabelFromKey renders the scope and complete host identity of a concurrency key.
func ScopeLabelFromKey(key string) string {
	if strings.HasPrefix(key, memoKeyPrefix) {
		return "content-cache"
	}
	scope, identity, _, ok := ParseConcurrencyKey(key)
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
