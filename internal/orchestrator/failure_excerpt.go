package orchestrator

import (
	"strings"
	"unicode/utf8"
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
