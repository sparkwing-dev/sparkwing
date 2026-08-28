package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var indexKeys = map[string]bool{
	"path":             true,
	"synopsis":         true,
	"subcommand_count": true,
	"hidden":           true,
}

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

func TestCommandsJSONStaysAnIndex(t *testing.T) {
	if n := len(commandsOutput(t, "-o", "json")); n > 40_000 {
		t.Errorf("commands -o json is %d bytes; an index that large is a help dump again", n)
	}
}

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
