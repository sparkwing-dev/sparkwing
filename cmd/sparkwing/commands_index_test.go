package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// indexKeys is the entire key set a `commands -o json` record may carry.
// Anything else means help text leaked back into the index.
var indexKeys = map[string]bool{
	"path":             true,
	"synopsis":         true,
	"subcommand_count": true,
	"hidden":           true,
}

// TestCommandsJSONCarriesIndexFieldsOnly is the acceptance criterion:
// the listing answers "which command", and every record carries the
// three fields that answer it. Description, flags, and examples answer
// "how do I call it", which `<path> --help` already answers from the
// same registry -- carrying them here was 83% of a 206KB payload and a
// second copy of the help system that could disagree with the first.
func TestCommandsJSONCarriesIndexFieldsOnly(t *testing.T) {
	out := commandsOutput(t, "-o", "json")
	records := decodeNDJSON[map[string]any](t, out)
	if len(records) == 0 {
		t.Fatal("commands -o json returned no records")
	}
	for i, rec := range records {
		for k := range rec {
			if !indexKeys[k] {
				t.Errorf("record %d (%v) carries non-index field %q", i, rec["path"], k)
			}
		}
		for _, k := range []string{"path", "synopsis", "subcommand_count"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("record %d (%v) is missing %q", i, rec["path"], k)
			}
		}
	}
}

// TestCommandsJSONSubcommandCountMatchesTheListing keeps the descend
// signal honest against the thing it is a signal about: the number has
// to equal the direct children the same listing emits, or a consumer
// that trusts it walks into an empty subtree (or misses a populated one).
func TestCommandsJSONSubcommandCountMatchesTheListing(t *testing.T) {
	records := decodeNDJSON[CommandIndexJSON](t, commandsOutput(t, "-o", "json"))
	children := map[string]int{}
	for _, c := range records {
		fields := strings.Fields(c.Path)
		if len(fields) < 2 {
			continue
		}
		children[strings.Join(fields[:len(fields)-1], " ")]++
	}
	groups := 0
	for _, c := range records {
		if got, want := c.SubcommandCount, children[c.Path]; got != want {
			t.Errorf("%q reports subcommand_count %d, listing has %d direct children", c.Path, got, want)
		}
		if c.SubcommandCount > 0 {
			groups++
		}
	}
	if groups == 0 {
		t.Fatal("no record reported any subcommands; the signal is dead")
	}
}

// TestCommandsJSONStaysAnIndex is a size regression guard. The bound is
// generous -- the listing is ~17KB today -- because it is not policing
// growth in the registry, only the return of a full-help field: the
// three that were dropped were 40KB, 55KB, and 76KB on their own.
func TestCommandsJSONStaysAnIndex(t *testing.T) {
	if n := len(commandsOutput(t, "-o", "json")); n > 40_000 {
		t.Errorf("commands -o json is %d bytes; an index that large is a help dump again", n)
	}
}

// TestCommandsJSONOmitsHiddenUnlessAsked pins the deliberate Hidden
// decision from both sides: hidden commands stay out of the listing
// (their help names the supported verb instead, so the index must not
// steer a reader at them), and --include-hidden both lists them and
// marks them, so a caller that opted in can tell which is which.
func TestCommandsJSONOmitsHiddenUnlessAsked(t *testing.T) {
	hidden := ""
	for _, c := range allCommands {
		if c.Hidden {
			hidden = c.Path
			break
		}
	}
	if hidden == "" {
		t.Skip("no hidden command in the registry")
	}

	for _, c := range decodeNDJSON[CommandIndexJSON](t, commandsOutput(t, "-o", "json")) {
		if c.Path == hidden {
			t.Fatalf("%q is Hidden but appears in the default listing", hidden)
		}
		if c.Hidden {
			t.Errorf("%q is flagged hidden in the default listing", c.Path)
		}
	}

	found := false
	for _, c := range decodeNDJSON[CommandIndexJSON](t, commandsOutput(t, "--include-hidden", "-o", "json")) {
		if c.Path != hidden {
			continue
		}
		found = true
		if !c.Hidden {
			t.Errorf("--include-hidden listed %q without marking it hidden", hidden)
		}
	}
	if !found {
		t.Errorf("--include-hidden did not list %q", hidden)
	}
}

// TestHelpJSONStillCarriesFullDetail is the other half of the split:
// dropping help text from the index is only sound because the detail
// page still has it. `<path> --help --json` is that page.
func TestHelpJSONStillCarriesFullDetail(t *testing.T) {
	var buf bytes.Buffer
	renderHelp(cmdCommands, []string{"--json"}, &buf)

	var got CommandJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode --help --json: %v\n%s", err, buf.String())
	}
	if got.Description == "" {
		t.Error("--help --json dropped the description")
	}
	if len(got.Flags) == 0 {
		t.Error("--help --json dropped the flags")
	}
	if len(got.Examples) == 0 {
		t.Error("--help --json dropped the examples")
	}
}
