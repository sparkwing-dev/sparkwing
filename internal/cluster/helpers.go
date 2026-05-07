package cluster

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// stdoutLogger is a local copy of orchestrator.stdoutLogger. Lives
// here so cluster code doesn't need to export a helper only cluster-
// side CLIs use. Forwards log lines to stdout/stderr with light level
// prefixes.
type stdoutLogger struct {
	mu sync.Mutex
}

func (s *stdoutLogger) Log(level, msg string) {
	s.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (s *stdoutLogger) Emit(rec sparkwing.LogRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := os.Stdout
	if rec.Level == "error" {
		out = os.Stderr
	}
	prefix := ""
	if rec.Node != "" {
		prefix = rec.Node + " │ "
	}
	switch rec.Event {
	case "node_start":
		fmt.Fprintf(out, "▶ %s\n", rec.Node)
	case "node_end":
		outcome, _ := rec.Attrs["outcome"].(string)
		fmt.Fprintf(out, "◀ %s %s\n", rec.Node, outcome)
	default:
		fmt.Fprintln(out, prefix+rec.Msg)
	}
}

// multiFlag collects repeated --flag values into a slice. Standard
// library Go equivalent of Cobra's StringArrayP. Local copy so the
// worker / runner CLI flag plumbing doesn't need orchestrator.MultiFlag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// sleepOrCancel waits d or until ctx is done, whichever comes first.
func sleepOrCancel(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// firstNonEmpty returns a if non-empty, otherwise b. Local copy so
// factories.go / worker_cli.go can pick a default URL without
// reaching into orchestrator.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// splitCSV splits a comma-separated string into a trimmed, non-empty
// string slice. Empty input returns nil so callers can distinguish
// "not set" from an empty list.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
