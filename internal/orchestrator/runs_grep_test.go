package orchestrator

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLogFile(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGrepNodeFile_MatchesSubstring(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "n.log")
	writeLogFile(t, tmp, []string{
		"starting",
		"ERROR: permission denied for /etc/x",
		"retrying",
		"ERROR: permission denied for /etc/y",
		"giving up",
	})
	got, err := grepNodeFile(tmp, "permission denied", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d matches, want 2", len(got))
	}
	if got[0].lineNo != 2 || got[1].lineNo != 4 {
		t.Errorf("line numbers off: %+v", got)
	}
}

func TestGrepNodeFile_MaxMatchesCap(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "n.log")
	writeLogFile(t, tmp, []string{"hit", "hit", "hit", "hit", "miss"})
	got, err := grepNodeFile(tmp, "hit", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("max-matches cap broken: %+v", got)
	}
}

func TestGrepNodeFile_NonexistentFileIsNoop(t *testing.T) {
	got, err := grepNodeFile("/no/such/path", "x", 0)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestEmitGrepMatches_QuietDedupAndSort(t *testing.T) {
	matches := []GrepMatch{
		{RunID: "run-b", NodeID: "n1", LineNo: 1, Line: "hit"},
		{RunID: "run-a", NodeID: "n1", LineNo: 2, Line: "hit"},
		{RunID: "run-a", NodeID: "n2", LineNo: 3, Line: "hit"},
	}
	var buf bytes.Buffer
	if err := emitGrepMatches(matches, GrepOpts{Quiet: true}, &buf); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(buf.String())
	if got != "run-a\nrun-b" {
		t.Errorf("quiet output = %q, want run-a\\nrun-b", got)
	}
}

func TestEmitGrepMatches_JSONShape(t *testing.T) {
	matches := []GrepMatch{
		{RunID: "run-a", NodeID: "build", LineNo: 7, Line: "ERROR: x"},
	}
	var buf bytes.Buffer
	if err := emitGrepMatches(matches, GrepOpts{JSON: true}, &buf); err != nil {
		t.Fatal(err)
	}
	var got []GrepMatch
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].RunID != "run-a" || got[0].LineNo != 7 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestEmitGrepMatches_TableHasHeader(t *testing.T) {
	matches := []GrepMatch{
		{RunID: "run-a", NodeID: "build", LineNo: 7, Line: "ERROR: x"},
	}
	var buf bytes.Buffer
	if err := emitGrepMatches(matches, GrepOpts{}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"RUN", "NODE", "LINE", "TEXT", "run-a", "ERROR: x"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestEmitGrepMatches_NoMatchesPrintsHint(t *testing.T) {
	var buf bytes.Buffer
	if err := emitGrepMatches(nil, GrepOpts{}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no matches") {
		t.Errorf("expected 'no matches' hint: %q", buf.String())
	}
}

func TestEmitGrepMatches_QuietJSONEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := emitGrepMatches(nil, GrepOpts{Quiet: true, JSON: true}, &buf); err != nil {
		t.Fatal(err)
	}
	out := strings.TrimSpace(buf.String())
	if out != "[]" {
		t.Errorf("expected []; got %q", out)
	}
}

func TestResolveRunLimit_DefaultsAndCap(t *testing.T) {
	if resolveRunLimit(GrepOpts{}) != grepDefaultRunLimit {
		t.Errorf("default limit wrong")
	}
	if got := resolveRunLimit(GrepOpts{Limit: 5000}); got != grepMaxRunLimit {
		t.Errorf("max cap not honored, got %d", got)
	}
	if got := resolveRunLimit(GrepOpts{Limit: 10}); got != 10 {
		t.Errorf("user limit wrong, got %d", got)
	}
}
