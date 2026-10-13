package nodehost_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var _ nodehost.Sink = (*nodehost.LogSink)(nil)

const logSecret = "hunter2-very-secret"

var hostNow = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

type recordLog struct {
	mu      sync.Mutex
	records []sparkwing.LogRecord
}

func (l *recordLog) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
}

type fakeState struct {
	mu  sync.Mutex
	err error
	ops []string
}

func (s *fakeState) note(op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = append(s.ops, op)
	return s.err
}

func (s *fakeState) AppendNodeAnnotation(_ context.Context, run, node, msg string) error {
	return s.note("node-annotation " + run + "/" + node + " " + msg)
}

func (s *fakeState) AppendStepAnnotation(_ context.Context, run, node, step, msg string) error {
	return s.note("step-annotation " + run + "/" + node + "/" + step + " " + msg)
}

func (s *fakeState) SetNodeSummary(_ context.Context, run, node, md string) error {
	return s.note("node-summary " + run + "/" + node + " " + md)
}

func (s *fakeState) SetStepSummary(_ context.Context, run, node, step, md string) error {
	return s.note("step-summary " + run + "/" + node + "/" + step + " " + md)
}

func newSink(t *testing.T, log nodehost.RecordWriter, state nodehost.NodeState, more ...string) *nodehost.LogSink {
	t.Helper()
	masker := secrets.NewMasker()
	for _, v := range append([]string{logSecret}, more...) {
		masker.Register(v)
	}
	sink, err := nodehost.NewLogSink(context.Background(), nodehost.LogSinkConfig{
		RunID: "run-1", NodeID: "build", Log: log, State: state, Masker: masker,
		Now: func() time.Time { return hostNow },
	})
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	return sink
}

func encoded(t *testing.T, records []sparkwing.LogRecord) string {
	t.Helper()
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func TestLogSinkMasksTextAndRecords(t *testing.T) {
	log := &recordLog{}
	sink := newSink(t, log, &fakeState{})
	sink.Text(nodehost.LevelInfo, "token="+logSecret)
	sink.Text(nodehost.LevelWarn, "stderr "+logSecret)
	sink.Record(nodeproto.LogRecord{TS: "x", Msg: "msg " + logSecret, Attrs: json.RawMessage(`{"nested":{"v":"` + logSecret + `"}}`)})
	sink.Flush()
	if got := encoded(t, log.records); strings.Contains(got, logSecret) || strings.Count(got, "***") != 4 {
		t.Fatalf("records not masked: %s", got)
	}
}

func TestLogSinkMasksSecretSplitAcrossLinePieces(t *testing.T) {
	log := &recordLog{}
	sink := newSink(t, log, &fakeState{})
	head := strings.Repeat("x", nodehost.MaxTextLine-4)
	sink.Text(nodehost.LevelInfo, head+logSecret[:4])
	sink.Text(nodehost.LevelInfo, logSecret[4:]+" tail")
	long := strings.Repeat("y", nodehost.MaxTextLine)
	sink.Text(nodehost.LevelWarn, long)
	sink.Flush()

	var info, warn strings.Builder
	for _, rec := range log.records {
		if rec.Level == nodehost.LevelInfo {
			info.WriteString(rec.Msg)
		} else {
			warn.WriteString(rec.Msg)
		}
	}
	if want := head + "*** tail"; info.String() != want {
		t.Fatalf("stdout pieces stored as %q...%q, want the secret masked whole", info.String()[:8], info.String()[len(head)-8:])
	}
	if warn.String() != long {
		t.Fatalf("held tail of an unfinished line not flushed: got %d bytes, want %d", warn.Len(), len(long))
	}
}

func TestLogSinkMasksAttrKeysNumbersAndNestedValues(t *testing.T) {
	log := &recordLog{}
	sink := newSink(t, log, &fakeState{}, "1234567")
	sink.Record(nodeproto.LogRecord{TS: "x", Attrs: json.RawMessage(`{"` + logSecret + `":"ok","n":3}`)})
	sink.Record(nodeproto.LogRecord{TS: "x", Attrs: json.RawMessage(`{"pin":1234567,"big":12345678}`)})
	sink.Record(nodeproto.LogRecord{TS: "x", Attrs: json.RawMessage(`{"a":{"b":["x","<` + logSecret + `>"]}}`)})
	got := encoded(t, log.records)
	if strings.Contains(got, logSecret) || strings.Contains(got, "1234567") {
		t.Fatalf("attrs not masked: %s", got)
	}
	if log.records[0].Attrs["***"] != "ok" || log.records[0].Attrs["n"] != json.Number("3") {
		t.Errorf("masked key record = %+v", log.records[0].Attrs)
	}
	if log.records[1].Attrs["masked_attrs"] != `{"big":***8,"pin":***}` {
		t.Errorf("unparseable masked attrs = %+v", log.records[1].Attrs)
	}
	if b, _ := log.records[2].Attrs["a"].(map[string]any)["b"].([]any); len(b) != 2 || b[1] != "<***>" {
		t.Errorf("nested secret not masked in place: %+v", log.records[2].Attrs)
	}
}

func TestLogSinkKeepsDropNoticesOutOfASplitStderrSecret(t *testing.T) {
	log := &recordLog{}
	sink := newSink(t, log, &fakeState{})
	head := strings.Repeat("x", nodehost.MaxTextLine-4)
	sink.Text(nodehost.LevelWarn, head+logSecret[:4])
	sink.Record(nodeproto.LogRecord{TS: "x", Level: "warn", Msg: "sparkwing: 3 log lines dropped while the log sink was behind"})
	sink.Text(nodehost.LevelWarn, logSecret[4:]+" tail")
	sink.Flush()
	var stderr strings.Builder
	for _, rec := range log.records {
		if !strings.Contains(rec.Msg, "dropped") {
			stderr.WriteString(rec.Msg)
		}
	}
	if stderr.String() != head+"*** tail" || strings.Contains(encoded(t, log.records), logSecret[4:]) {
		t.Fatal("drop notice broke the split stderr secret apart")
	}
}

func TestLogSinkHoldsAtMostOneValueOfOverlappingText(t *testing.T) {
	log := &recordLog{}
	sink := newSink(t, log, &fakeState{}, "aa")
	run := strings.Repeat("a", nodehost.MaxTextLine)
	for i := range 3 {
		sink.Text(nodehost.LevelInfo, run)
		if len(log.records) != i+1 || len(log.records[i].Msg) < nodehost.MaxTextLine {
			t.Fatalf("piece %d held back: %d records stored", i, len(log.records))
		}
	}
	sink.Flush()
	for _, rec := range log.records {
		if strings.Contains(rec.Msg, "aa") {
			t.Fatalf("overlapping secret stored unmasked in a %d-byte record", len(rec.Msg))
		}
	}
}

func TestLogSinkAttributesNodeStepAndHostTime(t *testing.T) {
	log := &recordLog{}
	sink := newSink(t, log, &fakeState{})
	sink.Record(nodeproto.LogRecord{TS: "1999-01-01T00:00:00Z", Level: "error", Node: "forged", Step: "compile", Msg: "boom"})
	sink.Record(nodeproto.LogRecord{TS: "1999-01-01T00:00:00Z", Level: "shout", Node: "forged", Msg: "loud"})
	sink.Text(nodehost.LevelInfo, "plain")
	want := []sparkwing.LogRecord{
		{TS: hostNow, Level: "error", JobID: "build", Step: "compile", Msg: "boom"},
		{TS: hostNow, Level: "info", JobID: "build", Msg: "loud"},
		{TS: hostNow, Level: "info", JobID: "build", Msg: "plain"},
	}
	if got, exp := encoded(t, log.records), encoded(t, want); got != exp {
		t.Fatalf("records:\n got %s\nwant %s", got, exp)
	}
}

func TestLogSinkPersistsAnnotationsAndSummaries(t *testing.T) {
	log, state := &recordLog{}, &fakeState{}
	sink := newSink(t, log, state)
	sink.Record(nodeproto.LogRecord{
		TS: "x", Event: "node_annotation", Msg: "ignored",
		Attrs: json.RawMessage(`{"message":"leaked ` + logSecret + `","level":"error","file":"main.go","line":4}`),
	})
	sink.Record(nodeproto.LogRecord{TS: "x", Event: "node_annotation", Step: "test", Msg: "from msg"})
	sink.Record(nodeproto.LogRecord{TS: "x", Event: "node_summary", Attrs: json.RawMessage(`{"markdown":"# done"}`)})
	sink.Record(nodeproto.LogRecord{TS: "x", Event: "node_summary", Step: "test", Attrs: json.RawMessage(`{"markdown":"step md"}`)})
	want := []string{
		"node-annotation run-1/build leaked ***",
		"step-annotation run-1/build/test from msg",
		"node-summary run-1/build # done",
		"step-summary run-1/build/test step md",
	}
	if strings.Join(state.ops, "\n") != strings.Join(want, "\n") {
		t.Fatalf("store writes:\n%s\nwant\n%s", strings.Join(state.ops, "\n"), strings.Join(want, "\n"))
	}
	if len(log.records) != 4 || log.records[0].Event != sparkwing.EventNodeAnnotation || log.records[0].Attrs["file"] != "main.go" {
		t.Fatalf("annotation records not stored with their event: %s", encoded(t, log.records))
	}
}

func TestLogSinkReportsFailedStateWrites(t *testing.T) {
	log := &recordLog{}
	sink := newSink(t, log, &fakeState{err: errors.New("store down: " + logSecret)})
	sink.Record(nodeproto.LogRecord{TS: "x", Event: "node_summary", Attrs: json.RawMessage(`{"markdown":"md"}`)})
	got := encoded(t, log.records)
	if len(log.records) != 2 || log.records[0].Event != "state_write_failed" || strings.Contains(got, logSecret) {
		t.Fatalf("failed write not reported masked: %s", got)
	}
}

func TestLogSinkStoresHostEventsFromTheSDKAsPlainMessages(t *testing.T) {
	log, state := &recordLog{}, &fakeState{}
	sink := newSink(t, log, state)
	for _, event := range []string{"step_start", "step_end", "step_skipped", "work_fail_fast", "node_start", "node_end", "retry", "invented"} {
		sink.Record(nodeproto.LogRecord{TS: "x", Event: event, Msg: event, Attrs: json.RawMessage(`{"outcome":"success"}`)})
	}
	sink.Record(nodeproto.LogRecord{TS: "x", Event: "exec_end", Attrs: json.RawMessage(`{"exit_code":0}`)})
	for _, rec := range log.records[:8] {
		if rec.Event != "" {
			t.Errorf("SDK record %q kept host event %q", rec.Msg, rec.Event)
		}
	}
	if last := log.records[8]; last.Event != "exec_end" {
		t.Errorf("SDK event exec_end not kept: %+v", last)
	}
	if len(state.ops) != 0 {
		t.Errorf("host events from the SDK reached the store: %v", state.ops)
	}
}

func TestNewLogSinkRequiresItsTargets(t *testing.T) {
	if _, err := nodehost.NewLogSink(context.Background(), nodehost.LogSinkConfig{RunID: "r", NodeID: "n", Log: &recordLog{}, State: &fakeState{}}); err == nil {
		t.Fatal("sink without a masker accepted")
	}
}

func readLines(t *testing.T, r io.Reader) []sparkwing.LogRecord {
	t.Helper()
	var out []sparkwing.LogRecord
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var rec sparkwing.LogRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("stored line %q: %v", sc.Text(), err)
		}
		out = append(out, rec)
	}
	return out
}

func writeThrough(t *testing.T, logs orchestrator.LogBackend) {
	t.Helper()
	nlog, err := logs.OpenNodeLog(context.Background(), "run-1", "build", nil)
	if err != nil {
		t.Fatalf("open node log: %v", err)
	}
	sink := newSink(t, nlog, &fakeState{})
	sink.Text(nodehost.LevelInfo, "out "+logSecret)
	sink.Record(nodeproto.LogRecord{TS: "x", Step: "s", Event: "step_end", Msg: "done"})
	sink.Flush()
	if err := nlog.Close(); err != nil {
		t.Fatalf("close node log: %v", err)
	}
}

func checkStored(t *testing.T, got []sparkwing.LogRecord) {
	t.Helper()
	if len(got) != 2 || got[0].Msg != "out ***" || got[0].JobID != "build" ||
		got[1].Event != "" || got[1].Step != "s" || !got[1].TS.Equal(hostNow) {
		t.Fatalf("stored records: %+v", got)
	}
}

func TestLogSinkWritesTheLocalRunDirectoryLog(t *testing.T) {
	paths := orchestrator.PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	writeThrough(t, orchestrator.LocalBackends(paths, nil, nil).Logs)
	raw, err := os.ReadFile(paths.NodeLog("run-1", "build"))
	if err != nil {
		t.Fatal(err)
	}
	checkStored(t, readLines(t, bytes.NewReader(raw)))
}

type fakeLogStore struct {
	storage.LogStore
	mu   sync.Mutex
	data bytes.Buffer
}

func (s *fakeLogStore) Append(_ context.Context, runID, nodeID string, data []byte) error {
	if runID != "run-1" || nodeID != "build" {
		return errors.New("wrong log " + runID + "/" + nodeID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Write(data)
	return nil
}

func TestLogSinkWritesTheControllerLogStore(t *testing.T) {
	store := &fakeLogStore{}
	writeThrough(t, orchestrator.NewLogStoreBackend(store, nil))
	checkStored(t, readLines(t, &store.data))
}

type textLog struct {
	recordLog
	text []sparkwing.LogRecord
}

func (l *textLog) EmitText(rec sparkwing.LogRecord) { l.text = append(l.text, rec) }

func TestLogSinkHandsPrintedTextToATextWriter(t *testing.T) {
	log := &textLog{}
	sink := newSink(t, log, &fakeState{})
	sink.Record(nodeproto.LogRecord{Level: "info", Msg: "from the sdk"})
	sink.Text(nodehost.LevelInfo, "printed "+logSecret)
	sink.Text(nodehost.LevelWarn, "to stderr")
	if got, exp := encoded(t, log.records), encoded(t, []sparkwing.LogRecord{{TS: hostNow, Level: "info", JobID: "build", Msg: "from the sdk"}}); got != exp {
		t.Errorf("records:\n got %s\nwant %s", got, exp)
	}
	want := []sparkwing.LogRecord{
		{TS: hostNow, Level: "info", JobID: "build", Msg: "printed ***"},
		{TS: hostNow, Level: "warn", JobID: "build", Msg: "to stderr"},
	}
	if got, exp := encoded(t, log.text), encoded(t, want); got != exp {
		t.Errorf("text:\n got %s\nwant %s", got, exp)
	}
}

// The controller refuses a summary body over 1 MiB, so an oversized summary is
// cut, after masking, to one that fits even with every byte JSON-escaped.
func TestLogSinkBoundsAnOversizedSummary(t *testing.T) {
	state := &fakeState{}
	sink := newSink(t, &recordLog{}, state)
	huge := strings.Repeat("\x01", 128<<10-200) + logSecret + strings.Repeat("\x01", 1<<20)
	sink.Record(nodeproto.LogRecord{Event: sparkwing.EventNodeSummary, Msg: huge})
	sink.Record(nodeproto.LogRecord{Event: sparkwing.EventNodeSummary, Step: "s", Msg: huge})
	sink.Record(nodeproto.LogRecord{Event: sparkwing.EventNodeSummary, Msg: "## small"})
	if len(state.ops) != 3 {
		t.Fatalf("ops = %d, want 3", len(state.ops))
	}
	for _, op := range state.ops[:2] {
		body, err := json.Marshal(map[string]string{"step_id": "s", "markdown": op})
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > 1<<20 || !strings.Contains(op, "summary truncated") || strings.Contains(op, logSecret[:8]) {
			t.Errorf("summary body is %d bytes, notes the cut %v, leaks the secret %v",
				len(body), strings.Contains(op, "summary truncated"), strings.Contains(op, logSecret[:8]))
		}
	}
	if state.ops[2] != "node-summary run-1/build ## small" {
		t.Errorf("small summary = %q, want it unchanged", state.ops[2])
	}
}

// An annotation goes through the same controller body limit as a summary.
func TestLogSinkBoundsAnOversizedAnnotation(t *testing.T) {
	state := &fakeState{}
	sink := newSink(t, &recordLog{}, state)
	huge := strings.Repeat("\x01", 128<<10-200) + logSecret + strings.Repeat("\x01", 1<<20)
	sink.Record(nodeproto.LogRecord{Event: sparkwing.EventNodeAnnotation, Msg: huge})
	sink.Record(nodeproto.LogRecord{Event: sparkwing.EventNodeAnnotation, Step: "s", Msg: huge})
	sink.Record(nodeproto.LogRecord{Event: sparkwing.EventNodeAnnotation, Msg: "built"})
	if len(state.ops) != 3 {
		t.Fatalf("ops = %d, want 3", len(state.ops))
	}
	for _, op := range state.ops[:2] {
		body, err := json.Marshal(map[string]string{"step_id": "s", "message": op})
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > 1<<20 || !strings.Contains(op, "annotation truncated") || strings.Contains(op, logSecret[:8]) {
			t.Errorf("annotation body is %d bytes, notes the cut %v, leaks the secret %v",
				len(body), strings.Contains(op, "annotation truncated"), strings.Contains(op, logSecret[:8]))
		}
	}
	if state.ops[2] != "node-annotation run-1/build built" {
		t.Errorf("small annotation = %q, want it unchanged", state.ops[2])
	}
}
