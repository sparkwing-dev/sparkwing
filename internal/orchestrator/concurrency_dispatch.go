package orchestrator

import (
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const memoKeyPrefix = "memo:"

func scopedGroupKey(g *sparkwing.ConcurrencyGroup, runID string) string {
	return nodeproto.ConcurrencyGroupKey(g.Name(), string(g.Limit().Scope), runID, boxHostID)
}

func qualifierFromKey(rest string) string {
	sep := strings.IndexByte(rest, nodeproto.ConcurrencyKeyLenSep[0])
	if sep < 0 {
		return ""
	}
	n, err := strconv.Atoi(rest[:sep])
	if err != nil || n < 0 || sep+1+n > len(rest) {
		return ""
	}
	return rest[sep+1 : sep+1+n]
}

func ScopeLabelFromKey(key string) string {
	switch {
	case strings.HasPrefix(key, memoKeyPrefix):
		return "content-cache"
	case strings.HasPrefix(key, nodeproto.ConcurrencyKeyGlobalPrefix):
		return "global"
	case strings.HasPrefix(key, nodeproto.ConcurrencyKeyRunPrefix):
		return "run (" + qualifierFromKey(key[len(nodeproto.ConcurrencyKeyRunPrefix):]) + ")"
	case strings.HasPrefix(key, nodeproto.ConcurrencyKeyBoxPrefix):
		return "box (" + qualifierFromKey(key[len(nodeproto.ConcurrencyKeyBoxPrefix):]) + ")"
	default:
		return "global"
	}
}

type copiedOutput struct {
	runID, nodeID string
}
