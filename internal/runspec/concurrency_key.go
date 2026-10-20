package runspec

import (
	"strconv"
	"strings"
)

// ParseConcurrencyKey recovers a scoped capacity key and its authored group.
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

// ConcurrencyKey encodes a resolved group with its authenticated scope identity.
func ConcurrencyKey(scope, identity, group string) string {
	if scope == "global" {
		return scope + ":" + group
	}
	return scope + ":" + strconv.Itoa(len(identity)) + ":" + identity + ":" + group
}
