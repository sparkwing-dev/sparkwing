package local

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type captureLogger struct {
	mu   sync.Mutex
	recs []sparkwing.LogRecord
}

func (c *captureLogger) Log(level, msg string) {
	c.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (c *captureLogger) Emit(rec sparkwing.LogRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, rec)
}

func (c *captureLogger) records() []sparkwing.LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sparkwing.LogRecord, len(c.recs))
	copy(out, c.recs)
	return out
}

func TestForwardRecords_ReplaysNDJSONAsRecords(t *testing.T) {
	stdout := strings.NewReader(`{"level":"info","msg":"compiling","event":"step_start"}` + "\n" +
		`{"level":"error","msg":"boom"}` + "\n")
	cap := &captureLogger{}
	forwardRecords(stdout, runner.Request{NodeID: "build", Delegate: cap}, slog.Default())

	got := cap.records()
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(got), got)
	}
	if got[0].Msg != "compiling" || got[0].Level != "info" || got[0].Event != "step_start" {
		t.Errorf("record 0 = %+v", got[0])
	}
	if got[1].Level != "error" || got[1].Msg != "boom" {
		t.Errorf("record 1 = %+v", got[1])
	}
	for i, r := range got {

		if r.TS.IsZero() {
			t.Errorf("record %d has no timestamp", i)
		}
		if r.JobID != "build" {
			t.Errorf("record %d JobID = %q, want build", i, r.JobID)
		}
	}
}

func TestForwardRecords_UndecodableLineIsForwardedAsWarn(t *testing.T) {
	stdout := strings.NewReader("total 42 files changed\n" +
		`{"level":"info","msg":"done"}` + "\n" +
		"{not json at all\n")
	cap := &captureLogger{}
	forwardRecords(stdout, runner.Request{NodeID: "build", Delegate: cap}, slog.Default())

	got := cap.records()
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3: %+v", len(got), got)
	}
	if got[0].Level != "warn" || got[0].Msg != "total 42 files changed" {
		t.Errorf("record 0 = %+v, want the raw line at warn", got[0])
	}
	if got[1].Level != "info" || got[1].Msg != "done" {
		t.Errorf("record 1 = %+v", got[1])
	}
	if got[2].Level != "warn" || !strings.Contains(got[2].Msg, "not json at all") {
		t.Errorf("record 2 = %+v, want the raw line at warn", got[2])
	}
}

func TestForwardRecords_SkipsBlankLinesAndPreservesGivenTimestamps(t *testing.T) {
	ts := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	stdout := strings.NewReader("\n   \n" +
		`{"ts":"2026-08-24T12:00:00Z","level":"info","msg":"kept"}` + "\n")
	cap := &captureLogger{}
	forwardRecords(stdout, runner.Request{NodeID: "build", Delegate: cap}, slog.Default())

	got := cap.records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(got), got)
	}
	if !got[0].TS.Equal(ts) {
		t.Errorf("TS = %s, want %s", got[0].TS, ts)
	}
}

func TestForwardStderr_EveryLineIsAWarning(t *testing.T) {
	cap := &captureLogger{}
	forwardStderr(strings.NewReader("warning: deprecated flag\n\nlink error\n"),
		runner.Request{NodeID: "build", Delegate: cap})

	got := cap.records()
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(got), got)
	}
	for i, r := range got {
		if r.Level != "warn" {
			t.Errorf("record %d level = %q, want warn", i, r.Level)
		}
		if r.JobID != "build" {
			t.Errorf("record %d JobID = %q, want build", i, r.JobID)
		}
	}
}

func TestForward_NilDelegateDrainsWithoutPanicking(t *testing.T) {
	forwardRecords(strings.NewReader("{\"msg\":\"x\"}\n"), runner.Request{NodeID: "b"}, slog.Default())
	forwardStderr(strings.NewReader("x\n"), runner.Request{NodeID: "b"})
}

func TestForwardRecords_OversizedLineDoesNotStopTheStream(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("x", 2<<20))
	b.WriteString("\n")
	for i := range 200 {
		fmt.Fprintf(&b, "{\"level\":\"info\",\"msg\":\"after-%d\"}\n", i)
	}

	cap := &captureLogger{}
	forwardRecords(strings.NewReader(b.String()), runner.Request{NodeID: "build", Delegate: cap}, slog.Default())

	got := cap.records()
	if len(got) != 201 {
		t.Fatalf("got %d records, want 201 (the oversized line plus 200 after it)", len(got))
	}
	if got[0].Level != "warn" || !strings.Contains(got[0].Msg, "truncated") {
		t.Errorf("record 0 = %+v, want a truncated warn record", got[0])
	}
	if got[200].Msg != "after-199" {
		t.Errorf("last record = %+v, want after-199", got[200])
	}
}

func TestForwardRecords_OversizedFinalLineIsTruncatedNotDropped(t *testing.T) {
	line := strings.Repeat("y", 1_100_000)
	cap := &captureLogger{}
	forwardRecords(strings.NewReader(line+"\n"), runner.Request{NodeID: "build", Delegate: cap}, slog.Default())

	got := cap.records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].Level != "warn" {
		t.Errorf("level = %q, want warn", got[0].Level)
	}
	kept := strings.TrimSuffix(got[0].Msg, truncationMarker)
	if len(kept) != maxLogLineBytes {
		t.Errorf("kept %d bytes, want the %d-byte cap", len(kept), maxLogLineBytes)
	}
	if got[0].Attrs["truncated"] != true {
		t.Errorf("Attrs = %v, want truncated:true", got[0].Attrs)
	}
}

func TestForwardRecords_TruncatedLineIsNotParsedAsJSON(t *testing.T) {
	huge := `{"level":"info","msg":"` + strings.Repeat("z", 2<<20) + `"}`
	cap := &captureLogger{}
	forwardRecords(strings.NewReader(huge+"\n"), runner.Request{NodeID: "build", Delegate: cap}, slog.Default())

	got := cap.records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].Level != "warn" {
		t.Errorf("level = %q, want warn (a fragment is not a record)", got[0].Level)
	}
}

func TestForwardStderr_OversizedLineDoesNotStopTheStream(t *testing.T) {
	body := strings.Repeat("e", 2<<20) + "\nrecovered\n"
	cap := &captureLogger{}
	forwardStderr(strings.NewReader(body), runner.Request{NodeID: "build", Delegate: cap})

	got := cap.records()
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[1].Msg != "recovered" {
		t.Errorf("second record = %+v, want the line after the oversized one", got[1])
	}
}

func TestForwardLines_ReassemblesAcrossReadBuffers(t *testing.T) {
	long := strings.Repeat("a", lineReadBufferBytes*3+17)
	var lines []string
	var flags []bool
	err := forwardLines(strings.NewReader("short\n"+long+"\ntail\n"), func(line []byte, truncated bool) {
		lines = append(lines, string(line))
		flags = append(flags, truncated)
	})
	if err != nil {
		t.Fatalf("forwardLines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if lines[0] != "short" || lines[2] != "tail" {
		t.Errorf("boundary lines = %q / %q", lines[0], lines[2])
	}
	if lines[1] != long {
		t.Errorf("long line reassembled to %d bytes, want %d", len(lines[1]), len(long))
	}
	for i, f := range flags {
		if f {
			t.Errorf("line %d flagged truncated; none exceeded the cap", i)
		}
	}
}

func TestForwardLines_UnterminatedFinalLineIsEmitted(t *testing.T) {
	var lines []string
	if err := forwardLines(strings.NewReader("a\nb"), func(line []byte, _ bool) {
		lines = append(lines, string(line))
	}); err != nil {
		t.Fatalf("forwardLines: %v", err)
	}
	if len(lines) != 2 || lines[1] != "b" {
		t.Fatalf("lines = %q, want [a b]", lines)
	}
}
