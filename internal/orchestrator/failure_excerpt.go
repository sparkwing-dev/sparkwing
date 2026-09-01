package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	failureExcerptMaxLines = 20
	failureExcerptMaxBytes = 4096
	failureHeadMaxBytes    = 200

	failureHeadCommandBytes = 100
)

func boundedFailureText(ctx context.Context, runID, nodeID string, err error) string {
	if err == nil {
		return ""
	}
	head, body, isOutput := splitFailure(err)
	head = boundedFailureHead(secrets.MaskCtx(ctx, head))
	body = secrets.MaskCtx(ctx, body)

	var b strings.Builder
	if head != "" {
		b.WriteString(head)
	}

	if !isOutput {

		msg, truncated := failureMessageHead(body, failureExcerptMaxLines, failureExcerptMaxBytes)
		if msg == "" {
			return b.String()
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(msg)
		if truncated {
			b.WriteString("\n… (message truncated)")
		}
		return b.String()
	}

	tail, truncated := failureExcerptTail(body, failureExcerptMaxLines, failureExcerptMaxBytes)
	if tail == "" {
		return b.String()
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	if truncated {
		b.WriteString(failureExcerptMarker(runID, nodeID))
		b.WriteString("\n")
	}
	b.WriteString(tail)
	return b.String()
}

func boundedFailureHead(head string) string {
	if head == "" {
		return ""
	}
	head = normalizeNewlines(head)
	first, rest, multiline := strings.Cut(head, "\n")
	elided := multiline && strings.TrimSpace(rest) != ""

	if len(first) <= failureHeadMaxBytes {
		first = strings.TrimRight(first, " \t")
		if elided {
			return first + " … (command truncated)"
		}
		return first
	}

	segs := strings.Split(first, ": ")
	if len(segs) == 1 {
		return strings.TrimRight(truncateUTF8(first, failureHeadMaxBytes), " \t") + " … (command truncated)"
	}

	kept := []string{truncateUTF8(segs[len(segs)-1], failureHeadCommandBytes)}
	used := len(kept[0])
	for i := len(segs) - 2; i >= 0; i-- {
		if used+len(segs[i])+len(": ") > failureHeadMaxBytes {
			break
		}
		used += len(segs[i]) + len(": ")
		kept = append(kept, segs[i])
	}
	slices.Reverse(kept)

	out := strings.TrimRight(strings.Join(kept, ": "), " \t")
	if len(kept) < len(segs) {
		out = "…: " + out
	}
	return out + " … (command truncated)"
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.RuneStart(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	if r, size := utf8.DecodeLastRuneInString(s); r == utf8.RuneError && size <= 1 {
		s = s[:len(s)-size]
	}
	return s
}

const nodeFailureExcerptEvent = "node_failure_excerpt"

type failureExcerpt struct {
	LogExcerpt string `json:"log_excerpt"`
	Truncated  bool   `json:"log_excerpt_truncated"`
}

func nodeFailureExcerpt(ctx context.Context, err error) (failureExcerpt, bool) {
	if err == nil {
		return failureExcerpt{}, false
	}
	_, body, isOutput := splitFailure(err)
	if !isOutput {
		return failureExcerpt{}, false
	}
	tail, truncated := failureExcerptTail(secrets.MaskCtx(ctx, body),
		failureExcerptMaxLines, failureExcerptMaxBytes)
	if tail == "" {
		return failureExcerpt{}, false
	}
	return failureExcerpt{LogExcerpt: tail, Truncated: truncated}, true
}

func appendFailureExcerptEvent(ctx context.Context, state StateBackend, runID, nodeID string, err error) {
	ex, ok := nodeFailureExcerpt(ctx, err)
	if !ok {
		return
	}
	payload, merr := json.Marshal(ex)
	if merr != nil {
		return
	}
	_ = state.AppendEvent(ctx, runID, nodeID, nodeFailureExcerptEvent, payload)
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

func splitFailure(err error) (head, body string, isOutput bool) {
	full := err.Error()
	var ee *sparkwing.ExecError
	if !errors.As(err, &ee) {
		return "", full, false
	}
	out := strings.TrimSpace(ee.Stderr)
	if out == "" {
		out = strings.TrimSpace(ee.Stdout)
	}
	if out == "" {
		return "", full, false
	}
	i := strings.LastIndex(full, out)
	if i < 0 {
		return full, out, true
	}
	head = strings.TrimRight(full[:i], "\n")
	if suffix := strings.TrimSpace(full[i+len(out):]); suffix != "" {
		head += " " + suffix
	}
	return head, out, true
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

func failureMessageHead(s string, maxLines, maxBytes int) (string, bool) {
	s = strings.Trim(normalizeNewlines(s), "\n")
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	truncated := false

	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	out := strings.Join(lines, "\n")

	if len(out) > maxBytes {
		out = truncateUTF8(out, maxBytes)

		if i := strings.LastIndexByte(out, '\n'); i > 0 {
			out = out[:i]
		}
		truncated = true
	}
	return out, truncated
}

func failureExcerptMarker(runID, nodeID string) string {
	switch {
	case runID != "" && nodeID != "":
		return fmt.Sprintf("… earlier output omitted (see: sparkwing runs logs --run %s --node %s)", runID, nodeID)
	case runID != "":
		return fmt.Sprintf("… earlier output omitted (see: sparkwing runs logs --run %s)", runID)
	default:
		return "… earlier output omitted (see the node log for the full output)"
	}
}
