package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// decodeNDJSON reads a list verb's `-o json` output the way a consumer
// now has to: a line at a time. It fails the test on the first line
// that is not a complete JSON value, which is the property the whole
// change exists to provide.
func decodeNDJSON[T any](t *testing.T, out string) []T {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var got []T
	for {
		var v T
		err := dec.Decode(&v)
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatalf("decode NDJSON record %d: %v\noutput:\n%s", len(got), err, out)
		}
		got = append(got, v)
	}
}

// TestCommandsJSONIsNDJSON is the acceptance criterion from the ticket:
// the discovery path AGENTS.md points a fresh agent at has to survive
// `head`. Truncating to five lines and parsing each one is exactly what
// a context-limited caller does, and the old pretty-printed array made
// it return nothing at all.
func TestCommandsJSONIsNDJSON(t *testing.T) {
	out := commandsOutput(t, "-o", "json")

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 5 {
		t.Fatalf("commands -o json emitted %d lines, want one per command", len(lines))
	}
	head := strings.Join(lines[:5], "\n") + "\n"
	five := decodeNDJSON[CommandIndexJSON](t, head)
	if len(five) != 5 {
		t.Fatalf("head -5 yielded %d records, want 5", len(five))
	}
	for i, c := range five {
		if c.Path == "" {
			t.Errorf("record %d decoded with no path: %+v", i, c)
		}
	}

	// Every line stands alone, and the stream carries the whole
	// registry rather than a truncated prefix of it.
	all := decodeNDJSON[CommandIndexJSON](t, out)
	if len(all) != len(lines) {
		t.Fatalf("decoded %d records from %d lines; a record spans more than its line", len(all), len(lines))
	}
	if strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Error("commands -o json still opens with an array")
	}
}

// TestCommandsJSONHonorsPathFilter keeps the NDJSON path wired to the
// same filter the other modes use -- a stream that ignores --path would
// hand back the whole 200KB surface to a caller that asked for a
// subtree, which is the failure this ticket is about.
func TestCommandsJSONHonorsPathFilter(t *testing.T) {
	records := decodeNDJSON[CommandIndexJSON](t, commandsOutput(t, "--path", "docs", "-o", "json"))
	if len(records) == 0 {
		t.Fatal("--path docs -o json returned no records")
	}
	for _, c := range records {
		if !strings.HasPrefix(c.Path, "sparkwing docs") {
			t.Errorf("--path docs leaked %q", c.Path)
		}
	}
}
