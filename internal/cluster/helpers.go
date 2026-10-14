package cluster

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

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

// safety: a runner identity that changed per request would hand the controller
// a fresh budget every poll and grow its bucket table at the fleet's poll rate,
// so a caller with no name of its own names this process instead.
func processRunnerIdentity(role string) string {
	return client.ProcessIdentity(role)
}

// safety: two runner processes on one host are given the same holder prefix, so
// the process id is what tells the controller's per-runner budgets and its idle
// poll gate that they are two runners rather than one runner polling twice.
func holderRunnerIdentity(prefix string) string {
	return prefix + ":" + strconv.Itoa(os.Getpid())
}
