package orchestrator

import (
	"strings"
)

const memoKeyPrefix = "memo:"

func ScopeLabelFromKey(key string) string {
	switch {
	case strings.HasPrefix(key, memoKeyPrefix):
		return "content-cache"
	case strings.HasPrefix(key, "global:"):
		return "global"
	case strings.HasPrefix(key, "run:"):
		return "run (" + strings.SplitN(key[len("run:"):], ":", 2)[0] + ")"
	case strings.HasPrefix(key, "box:"):
		return "box (" + strings.SplitN(key[len("box:"):], ":", 2)[0] + ")"
	default:
		return "global"
	}
}

type copiedOutput struct {
	runID, nodeID string
}
