package runspec

import (
	"strconv"
	"strings"
)

// ParseConcurrencyKey recovers the admission level, scope and authored group.
func ParseConcurrencyKey(key string) (level, scope, identity, group string, ok bool) {
	level, key, found := strings.Cut(key, ":")
	if !found || level != "plan" && level != "job" {
		return "", "", "", "", false
	}
	scope, rest, found := strings.Cut(key, ":")
	if !found {
		return "", "", "", "", false
	}
	if scope == "global" {
		return level, scope, "", rest, true
	}
	if scope != "run" && scope != "box" {
		return "", "", "", "", false
	}
	size, rest, found := strings.Cut(rest, ":")
	n, err := strconv.Atoi(size)
	if !found || err != nil || n < 0 || n >= len(rest) || strconv.Itoa(n) != size || rest[n] != ':' {
		return "", "", "", "", false
	}
	return level, scope, rest[:n], rest[n+1:], true
}

// ConcurrencyKey encodes a resolved group with its authenticated scope identity.
func ConcurrencyKey(level, scope, identity, group string) string {
	if scope == "global" {
		return level + ":" + scope + ":" + group
	}
	return level + ":" + scope + ":" + strconv.Itoa(len(identity)) + ":" + identity + ":" + group
}
