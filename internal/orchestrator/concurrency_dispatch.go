package orchestrator

import (
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

const memoKeyPrefix = "memo:"

func ScopeLabelFromKey(key string) string {
	if strings.HasPrefix(key, memoKeyPrefix) {
		return "content-cache"
	}
	scope, identity, _, ok := nodehost.ParseConcurrencyKey(key)
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
