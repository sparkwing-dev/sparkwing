package cluster

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
	if rec.JobID != "" {
		prefix = rec.JobID + " │ "
	}
	switch rec.Event {
	case "node_start":
		fmt.Fprintf(out, "▶ %s\n", rec.JobID)
	case "node_end":
		outcome, _ := rec.Attrs["outcome"].(string)
		fmt.Fprintf(out, "◀ %s %s\n", rec.JobID, outcome)
	default:
		fmt.Fprintln(out, prefix+rec.Msg)
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func sleepOrCancel(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

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
