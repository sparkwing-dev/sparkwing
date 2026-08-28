package wingd_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type logSink struct {
	mu      sync.Mutex
	lines   []string
	changed chan struct{}
}

func (s *logSink) logf(format string, args ...any) {
	s.mu.Lock()
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *logSink) matching(re *regexp.Regexp) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.lines {
		if re.MatchString(l) {
			out = append(out, l)
		}
	}
	return out
}

var disconnectLine = regexp.MustCompile(`^conn (\d+) disconnected while `)

func TestDaemon_LogsDistinctConnIDsPerClient(t *testing.T) {
	home := shortHome(t)
	sink := &logSink{changed: make(chan struct{}, 1)}
	startDaemon(t, wingd.Config{
		Home: home, Version: "v1", GraceWindow: -1, HeadroomFraction: -1,
		Logf: sink.logf,
	})

	first := ensure(t, home, "v1")
	if _, err := first.Acquire(context.Background(), semReq("run-a", "lock-a", 1, 1, wingwire.PolicyQueue), nil); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	second := ensure(t, home, "v1")
	if _, err := second.Acquire(context.Background(), semReq("run-b", "lock-b", 1, 1, wingwire.PolicyQueue), nil); err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	_ = first.Close()
	_ = second.Close()

	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	var lines []string
	for {
		lines = sink.matching(disconnectLine)
		if len(lines) >= 2 {
			break
		}
		select {
		case <-sink.changed:
		case <-timeout.C:
			t.Fatalf("want a disconnect line per client, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
		}
	}

	ids := map[string]string{}
	for _, l := range lines {
		id := disconnectLine.FindStringSubmatch(l)[1]
		if prev, dup := ids[id]; dup {
			t.Fatalf("two connections logged the same id %s:\n  %s\n  %s", id, prev, l)
		}
		ids[id] = l
	}

	for _, want := range []string{"run-a", "run-b"} {
		found := false
		for _, l := range lines {
			if strings.Contains(l, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no disconnect line named %s:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}
