//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const splitSecret = "hunter2-straddles-the-split"

type maskLog struct {
	mu      sync.Mutex
	records []sparkwing.LogRecord
}

func (l *maskLog) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
}

func (l *maskLog) Close() error { return nil }

func (l *maskLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, r := range l.records {
		raw, _ := json.Marshal(r)
		b.Write(raw)
		b.WriteString(r.Msg)
	}
	return b.String()
}

// safety: the entry prints a line whose first 1 MiB ends ten bytes into the secret, the session's split point.
func splitLineEntry(t *testing.T) pipelineentry.Command {
	t.Helper()
	entry := filepath.Join(t.TempDir(), "split-line")
	script := "#!/bin/sh\nhead -c 1048566 /dev/zero | tr '\\000' x\nprintf '" + splitSecret + " tail\\n'\n"
	if err := os.WriteFile(entry, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return pipelineentry.Binary(entry)
}

// A secret straddling the session's split of a long stdout line reaches the
// log masked whole.
func TestSessionMasksASecretSplitAtTheLineLimit(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	log := &maskLog{}
	sink, err := NewLogSink(t.Context(), LogSinkConfig{RunID: "r", NodeID: "n", Log: log, State: openStore(t), Masker: masker})
	if err != nil {
		t.Fatal(err)
	}
	s, err := StartSession(t.Context(), SessionConfig{
		Entry: splitLineEntry(t), Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, Sink: sink, Grace: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Close()
	sink.Flush()
	got := log.joined()
	if strings.Contains(got, splitSecret[:10]) || !strings.Contains(got, "*** tail") {
		t.Fatalf("log ends %q, want the split secret masked whole", got[max(0, len(got)-80):])
	}
}

// A note the host writes to a node's log is masked in its message and in
// every attribute, nested ones included.
func TestHostNodeLogMasksMessageAndAttrs(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	log := &maskLog{}
	cfg := &Config{Attempts: &Attempts{Masker: masker, OpenLog: func(context.Context, string, string) (NodeLog, error) { return log, nil }}}
	n := &NodeRun{Run: &Run{ID: "r", Logger: slog.New(slog.DiscardHandler)}, Plan: nodeproto.PlanNode{ID: "n"}}
	hostNodeLog(t.Context(), cfg, n, sparkwing.LogRecord{
		Level: "info", Msg: "retry " + splitSecret,
		Attrs: map[string]any{"attempt": 2, "why": map[string]any{"err": "<" + splitSecret + ">"}},
	})
	if got := log.joined(); strings.Contains(got, splitSecret) || strings.Count(got, "***") < 3 {
		t.Fatalf("host note stored as %s", got)
	}
	if log.records[0].Attrs["attempt"] == nil {
		t.Fatalf("attrs without a secret lost: %+v", log.records[0].Attrs)
	}
}
