//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

// A note the host writes to a node's log is masked in its message and in
// every attribute, nested ones included.
func TestHostNodeLogMasksMessageAndAttrs(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	log := &maskLog{}
	cfg := &Config{Attempts: &Attempts{Masker: masker, OpenLog: func(context.Context, string, string) (NodeLog, error) { return log, nil }}}
	n := &NodeRun{Run: &Run{ID: "r", Logger: slog.New(slog.DiscardHandler), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}, Plan: nodeproto.JobSpec{ID: "n", Steps: []nodeproto.StepSpec{{ID: "body"}}}}
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

// A plan the SDK refuses quoting a secret default comes back from planning
// masked, so every caller records and prints it masked without masking again.
func TestPlanningErrorsComeBackMasked(t *testing.T) {
	const secret = "plan-secret-4242"
	script := `IFS= read -r line <&3
id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
printf '{"reply":"%s","ok":true,"result":{"pipelines":[{"name":"demo","args":[{"name":"tok","type":"string","required":false,"secret":true,"default":"` + secret + `"}]}]}}\n' "$id" >&4
IFS= read -r line <&3
id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
printf '{"reply":"%s","ok":false,"error":{"code":"plan_failed","message":"plan panicked: ` + secret + `"}}\n' "$id" >&4
IFS= read -r line <&3
`
	err := describeFails(t, script, "plan", discardSink{})
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "plan panicked: ***") {
		t.Fatalf("err = %v, want the plan failure masked", err)
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

// safety: the secret sits where the bound cuts the headline, so a bound taken
// before masking would keep a fragment the masker no longer recognizes.
func outerLayerFailure() error {
	return errors.New(strings.Repeat("h", nodeErrTextHeadBytes-10) + splitSecret + " slot full\n" + strings.Repeat("y", 2*nodeErrTextMaxBytes))
}

func checkMaskedNodeFailure(t *testing.T, st *textStore, res Result) {
	t.Helper()
	if st.nodeError == "" || st.failedEvent == "" || res.Err == nil {
		t.Fatal("failure did not reach every result sink")
	}
	for what, text := range map[string]string{"row": st.nodeError, "events": st.failedEvent, "result": res.Err.Error()} {
		if strings.Contains(text, splitSecret[:10]) {
			t.Errorf("%s holds a piece of the secret", what)
		}
	}
}

// A failure from a layer outside the attempt, such as admission refusing a
// concurrency key that holds a secret, is masked, then bounded, wherever a
// node's result is recorded.
func TestOuterLayerNodeFailuresAreMaskedThenBounded(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	t.Run("scheduler", func(t *testing.T) {
		st := &textStore{}
		cfg := Config{State: st, Attempts: &Attempts{Masker: masker}, Run: nodeproto.RunContext{RunID: "r"}, Logger: slog.New(slog.DiscardHandler)}
		s := &scheduler{run: newRun(&cfg, nodeproto.PlanDoc{}, nil, nil), results: map[string]Result{}}
		s.finish(t.Context(), "n", Result{Outcome: sparkwing.Failed, Err: outerLayerFailure()})
		checkMaskedNodeFailure(t, st, s.results["n"])
	})
}

type textStore struct {
	storage.StateStoreOld
	mu                     sync.Mutex
	writes                 []string
	nodeError, failedEvent string
}

func (s *textStore) note(parts ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, strings.Join(parts, " "))
	return nil
}

func (s *textStore) FinishRun(_ context.Context, _, _, errMsg string) error {
	return s.note(errMsg)
}

func (s *textStore) FinishNode(_ context.Context, _, _, _, errMsg string, _ []byte) error {
	return s.note(errMsg)
}

func (s *textStore) FinishNodeWithReason(_ context.Context, _, _, _, errMsg string, _ []byte, _ string, _ *int) error {
	s.nodeError = errMsg
	return s.note(errMsg)
}

func (s *textStore) UpdateNodeActivity(_ context.Context, _, _, detail string) error {
	return s.note(detail)
}

func (s *textStore) AppendNodeAnnotation(_ context.Context, _, _, msg string) error {
	return s.note(msg)
}

func (s *textStore) SetNodeSummary(_ context.Context, _, _, md string) error {
	return s.note(md)
}

func (s *textStore) AppendStepAnnotation(_ context.Context, _, _, _, msg string) error {
	return s.note(msg)
}

func (s *textStore) SetStepSummary(_ context.Context, _, _, _, md string) error {
	return s.note(md)
}

func (s *textStore) AppendEvent(_ context.Context, _, _, kind string, payload []byte) error {
	if kind == "node_failed" {
		s.failedEvent = string(payload)
	}
	return s.note(string(payload))
}

func (s *textStore) CreateApproval(_ context.Context, a store.Approval) error {
	return s.note(a.Message)
}

// Every text a run writes to its store is masked on the way in, whichever
// source forgot to: an approval's message and its approval_requested event,
// annotations, summaries, activity and error text.
func TestTheRunsStoreMasksEveryTextItIsGiven(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	inner := &textStore{}
	st := maskState(inner, masker)
	ctx, s := t.Context(), "x "+splitSecret
	_ = st.FinishRun(ctx, "r", "failed", s)
	_ = st.FinishNode(ctx, "r", "n", "failed", s, nil)
	_ = st.FinishNodeWithReason(ctx, "r", "n", "failed", s, nil, "", nil)
	_ = st.UpdateNodeActivity(ctx, "r", "n", s)
	_ = st.AppendNodeAnnotation(ctx, "r", "n", s)
	_ = st.SetNodeSummary(ctx, "r", "n", s)
	_ = st.AppendStepAnnotation(ctx, "r", "n", "s", s)
	_ = st.SetStepSummary(ctx, "r", "n", "s", s)
	_ = st.AppendEvent(ctx, "r", "n", "approval_requested", []byte(`{"message":"`+s+`"}`))
	_ = st.AppendEvent(ctx, "r", "n", "node_failed", []byte(s))
	_ = st.CreateApproval(ctx, store.Approval{RunID: "r", NodeID: "n", Message: s})
	if len(inner.writes) != 11 {
		t.Fatalf("%d writes reached the store, want 11", len(inner.writes))
	}
	for _, w := range inner.writes {
		if strings.Contains(w, splitSecret) || !strings.Contains(w, "***") {
			t.Errorf("store got %q", w)
		}
	}
}

// safety: refuses the first output write with an error quoting the secret
// where the bound cuts the headline, as a storage failure can.
type refusingOutputStore struct {
	storage.StateStoreOld
	refused bool
}

func (s *refusingOutputStore) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	if !s.refused && output != nil {
		s.refused = true
		lead := strings.Repeat("h", nodeErrTextHeadBytes-10-len(store.ErrOutputNotStored.Error()+": "))
		return fmt.Errorf("%w: %s", store.ErrOutputNotStored, lead+splitSecret+"\n"+strings.Repeat("y", 2*nodeErrTextMaxBytes))
	}
	return s.StateStoreOld.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

// An output the store refuses replaces a masked result with the store's
// error, which is masked before it is bounded and returned.
func TestARefusedOutputsErrorIsMaskedThenBounded(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	ok := Result{Outcome: sparkwing.Success, Output: []byte(`{"v":1}`)}
	t.Run("scheduler", func(t *testing.T) {
		st := &textStore{}
		cfg := Config{State: maskState(&refusingOutputStore{StateStoreOld: st}, masker), Attempts: &Attempts{Masker: masker}, Run: nodeproto.RunContext{RunID: "r"}, Logger: slog.New(slog.DiscardHandler)}
		s := &scheduler{run: newRun(&cfg, nodeproto.PlanDoc{}, nil, nil), results: map[string]Result{}}
		s.finish(t.Context(), "n", ok)
		checkMaskedNodeFailure(t, st, s.results["n"])
	})
}

// A run's host diagnostics, such as a generator's error or a queued
// concurrency key, are masked in the message and in every attribute.
func TestARunsLoggerMasksItsDiagnostics(t *testing.T) {
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	var out strings.Builder
	logger := maskLogger(slog.New(slog.NewTextHandler(&out, nil)), masker).With("key", "k-"+splitSecret)
	logger.Error("generator "+splitSecret, "err", errors.New("boom "+splitSecret), slog.Group("g", "keys", []string{splitSecret}))
	if got := out.String(); strings.Contains(got, splitSecret) || strings.Count(got, "***") < 4 {
		t.Fatalf("logged %s", got)
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
