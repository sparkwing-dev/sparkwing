package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
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

// A listing carries what a reader needs to choose; the read verb carries the
// content (house rule 13). `pipeline list` shipped every pipeline's full help
// text and examples, which made a five-entry catalog 2.7KB of prose nobody had
// asked for -- and `pipeline describe` was already the place to ask.
func TestPipelineListIsAnIndexAndDescribeKeepsTheDetail(t *testing.T) {
	full := Pipeline{
		Name:       "release",
		Short:      "Tag and publish",
		Help:       "A long help essay that belongs behind describe.",
		Entrypoint: "Release",
		Triggers:   []string{"push:main"},
		Examples:   []sparkwing.Example{{Comment: "Run it", Command: "sparkwing run release"}},
		Risks:      []string{"publishes"},
	}
	line, err := json.Marshal(full.index())
	if err != nil {
		t.Fatal(err)
	}
	for _, detail := range []string{"help", "examples", "risks", "args", "env_vars"} {
		if strings.Contains(string(line), `"`+detail+`"`) {
			t.Errorf("the listing carries %q, which describe already answers: %s", detail, line)
		}
	}
	for _, choosing := range []string{"release", "Tag and publish", "Release", "push:main"} {
		if !strings.Contains(string(line), choosing) {
			t.Errorf("the listing dropped %q, which a caller chooses by: %s", choosing, line)
		}
	}
	// Dropping the help is only sound because a pipeline that has nothing else
	// to say still gets a line worth reading.
	bare := Pipeline{Name: "quiet", Help: "first line\nsecond line"}
	if got := bare.index().Short; got != "first line" {
		t.Errorf("a pipeline with no short summarized as %q, want the first line of its help", got)
	}
}

// The token is redacted in the machine-readable listing too. A JSON listing is
// the shape most likely to be piped into a log, and a secret that leaves the
// process once has left it.
func TestProfilesListStreamsRedactedRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.yaml")
	if err := os.WriteFile(path, []byte(
		"profiles:\n  prod:\n    controller:\n      url: https://example.invalid\n      token: swu_supersecretvalue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_PROFILES", path)

	out := captureStdout(t, func() {
		if err := runProfilesList([]string{"-o", "json"}); err != nil {
			t.Errorf("profiles list: %v", err)
		}
	})
	records := decodeNDJSON[profileIndex](t, out)
	if len(records) != 1 || records[0].Name != "prod" {
		t.Fatalf("profiles list streamed %+v", records)
	}
	if strings.Contains(out, "supersecretvalue") {
		t.Errorf("the listing printed the raw token: %s", out)
	}
	if records[0].Controller != "https://example.invalid" {
		t.Errorf("record dropped the controller a caller picks by: %+v", records[0])
	}
}
