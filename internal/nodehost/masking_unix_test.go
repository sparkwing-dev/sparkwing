//go:build unix

package nodehost

import (
	"encoding/json"
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
	script := "#!/bin/sh\n" + testHelloScript + "head -c 1048566 /dev/zero | tr '\\000' x\nprintf '" + splitSecret + " tail\\n'\n"
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
	sink, err := NewLogSink(t.Context(), LogSinkConfig{RunID: "r", NodeID: "n", Log: log, Masker: masker})
	if err != nil {
		t.Fatal(err)
	}
	s, err := StartSession(t.Context(), SessionConfig{
		Entry: splitLineEntry(t), Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, Sink: sink, Masker: masker, Grace: 2 * time.Second, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Close()
	got := log.joined()
	if strings.Contains(got, splitSecret[:10]) || !strings.Contains(got, "*** tail") {
		t.Fatalf("log ends %q, want the split secret masked whole", got[max(0, len(got)-80):])
	}
}

type queueSink struct {
	mu      sync.Mutex
	text    []string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *queueSink) Text(_, line string) {
	if s.entered != nil {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text = append(s.text, line)
}

func (s *queueSink) Record(rec nodeproto.FrameLog) { s.Text("", rec.Message) }

func (s *queueSink) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.text, "")
}

// A drop notice between the pieces of a split stderr line is a record, so it
// never breaks the secret the pieces carry apart.
func TestSinkQueueMasksASplitSecretAroundADropNotice(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	sink := &queueSink{}
	q := newSinkQueue(LevelWarn, masker)
	head := strings.Repeat("x", maxTextLine-4)
	q.push(sinkItem{text: head + splitSecret[:4], piece: true})
	q.notice(nodeproto.FrameLog{Message: "sparkwing: 3 log lines dropped while the log sink was behind"})
	q.push(sinkItem{text: splitSecret[4:] + " tail"})
	q.close()
	q.run(sink)
	if got := strings.ReplaceAll(sink.joined(), "sparkwing: 3 log lines dropped while the log sink was behind", ""); got != head+"*** tail" {
		t.Fatalf("stderr ends %q, want the split secret masked whole", got[max(0, len(got)-40):])
	}
}

// A queue the session abandons while the head of a split secret is held
// shows neither the held tail nor the rest of the line delivered after.
func TestAnAbandonedSinkQueueShowsNoPieceOfAHeldSecret(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	sink := &queueSink{entered: make(chan struct{}), release: make(chan struct{})}
	q := newSinkQueue(LevelInfo, masker)
	go q.run(sink)
	q.push(sinkItem{text: strings.Repeat("x", maxTextLine-10) + splitSecret[:10], piece: true})
	<-sink.entered
	q.push(sinkItem{text: splitSecret[10:] + " tail"})
	q.close()
	if lost := q.end(true); lost != 2 {
		t.Errorf("abandoned queue lost %d items, want the one in delivery and the one queued", lost)
	}
	close(sink.release)
	<-q.done
	if got := sink.joined(); strings.Contains(got, splitSecret[:10]) || strings.Contains(got, splitSecret[10:]) {
		t.Fatalf("an abandoned queue showed a piece of the secret: ...%q", got[max(0, len(got)-40):])
	}
}

// A line of exactly the session's line limit with no newline ends on a piece
// and a clean EOF; its held tail still reaches the sink.
func TestSessionDeliversAPieceEndingExactlyAtEOF(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	log := &maskLog{}
	sink, err := NewLogSink(t.Context(), LogSinkConfig{RunID: "r", NodeID: "n", Log: log, Masker: masker})
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(t.TempDir(), "exact-line")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"+testHelloScript+"head -c 1048576 /dev/zero | tr '\\000' x\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := StartSession(t.Context(), SessionConfig{
		Entry: pipelineentry.Binary(entry), Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, Sink: sink, Masker: masker, Grace: 2 * time.Second, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Close()
	n := 0
	for _, r := range log.records {
		n += len(r.Msg)
	}
	if n != maxTextLine {
		t.Fatalf("delivered %d bytes of a %d-byte line", n, maxTextLine)
	}
}

// When the queue drops the rest of a split line, the head of a secret the
// delivered piece ended on is discarded, never joined to a later line.
func TestSinkQueueDiscardsAHeldSecretHeadWhenTheRestIsDropped(t *testing.T) {
	defer func(old int) { sinkQueueBytes = old }(sinkQueueBytes)
	sinkQueueBytes = maxTextLine + 100
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	sink := &queueSink{entered: make(chan struct{}), release: make(chan struct{})}
	q := newSinkQueue(LevelInfo, masker)
	q.push(sinkItem{text: strings.Repeat("x", maxTextLine-10) + splitSecret[:10], piece: true, size: maxTextLine})
	q.push(sinkItem{text: splitSecret[10:] + " rest", size: 40})
	go q.run(sink)
	<-sink.entered
	close(sink.release)
	q.push(sinkItem{text: "next", size: 4})
	q.close()
	<-q.done
	if got := sink.joined(); strings.Contains(got, splitSecret[:10]) || !strings.Contains(got, "next") {
		t.Fatalf("output ends %q, want the held head discarded and the next line kept", got[max(0, len(got)-60):])
	}
}

// A CRLF split across the session's piece boundary ends its line as one,
// with no carriage return left in the text.
func TestSessionNormalizesACRLFSplitAtTheLineLimit(t *testing.T) {
	log := &maskLog{}
	sink, err := NewLogSink(t.Context(), LogSinkConfig{RunID: "r", NodeID: "n", Log: log, Masker: secrets.NewMasker()})
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(t.TempDir(), "crlf-line")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"+testHelloScript+"head -c 1048575 /dev/zero | tr '\\000' x\nprintf '\\r\\nnext\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := StartSession(t.Context(), SessionConfig{
		Entry: pipelineentry.Binary(entry), Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, Sink: sink, Grace: 2 * time.Second, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Close()
	got := log.joined()
	if strings.Contains(got, "\r") || !strings.Contains(got, "next") {
		t.Fatalf("a CRLF split at the limit left a carriage return or lost the next line")
	}
}
