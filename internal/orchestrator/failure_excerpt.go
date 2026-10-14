package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	failureExcerptMaxLines = 20
	failureExcerptMaxBytes = 4096
	failureHeadMaxBytes    = 200
)

const nodeFailureExcerptEvent = "node_failure_excerpt"

type failureExcerpt struct {
	LogExcerpt string `json:"log_excerpt"`
	Truncated  bool   `json:"log_excerpt_truncated"`
}

type eventLister interface {
	ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error)
}

const (
	excerptPageSize = 1000
	excerptMaxPages = 50
)

type failureExcerptIndex struct {
	byNode     map[string]failureExcerpt
	Incomplete bool
}

func (ix failureExcerptIndex) Get(nodeID string) (failureExcerpt, bool) {
	ex, ok := ix.byNode[nodeID]
	return ex, ok
}

func (ix failureExcerptIndex) Unavailable(nodeID string) bool {
	if !ix.Incomplete {
		return false
	}
	_, ok := ix.byNode[nodeID]
	return !ok
}

func failureExcerptsFor(ctx context.Context, src eventLister, runID string, want map[string]struct{}) failureExcerptIndex {
	if src == nil || runID == "" || len(want) == 0 {
		return failureExcerptIndex{}
	}
	ix := failureExcerptIndex{byNode: map[string]failureExcerpt{}, Incomplete: true}
	var after int64
	for range excerptMaxPages {
		events, err := src.ListEventsAfter(ctx, runID, after, excerptPageSize)
		if err != nil {
			return ix
		}
		for _, e := range events {
			after = max(after, e.Seq)
			if e.Kind != nodeFailureExcerptEvent || e.NodeID == "" || len(e.Payload) == 0 {
				continue
			}
			if _, wanted := want[e.NodeID]; !wanted {
				continue
			}
			var ex failureExcerpt
			if json.Unmarshal(e.Payload, &ex) != nil || ex.LogExcerpt == "" {
				continue
			}
			ix.byNode[e.NodeID] = ex
		}
		if len(ix.byNode) == len(want) || len(events) < excerptPageSize {
			ix.Incomplete = false
			return ix
		}
	}
	return ix
}

func failedNodeIDs(nodes []*store.Node) map[string]struct{} {
	var out map[string]struct{}
	for _, n := range nodes {
		if n == nil || n.Outcome != string(sparkwing.Failed) {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, 1)
		}
		out[n.NodeID] = struct{}{}
	}
	return out
}

func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func failureExcerptTail(s string, maxLines, maxBytes int) (string, bool) {
	s = strings.TrimRight(normalizeNewlines(s), "\n")
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	truncated := false

	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		truncated = true
	}
	out := strings.Join(lines, "\n")

	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		} else {
			for len(out) > 0 && !utf8.RuneStart(out[0]) {
				out = out[1:]
			}
		}
		truncated = true
	}
	if strings.TrimSpace(out) == "" {
		return "", truncated
	}
	return out, truncated
}
