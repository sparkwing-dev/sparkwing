package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestAllCommandsAreRegistered(t *testing.T) {
	expected := map[string]bool{}
	for _, command := range clisurface.Registry() {
		if command.Path == "sparkwing" || strings.HasPrefix(command.Path, "sparkwing ") {
			expected[command.Path] = true
		}
	}
	for _, command := range allCommands {
		if !expected[command.Path] {
			t.Errorf("discovery exposes an unknown or duplicate command %q", command.Path)
		}
		delete(expected, command.Path)
	}
	for path := range expected {
		t.Errorf("discovery omits %q", path)
	}
}

func TestDiscoveryRejectsRemovedFamilies(t *testing.T) {
	for _, path := range []string{"cluster", "cloud", "configure", "serve", "crons", "debug run", "debug release", "runs consumer", "pipeline sparks resolve"} {
		if err := runCommandsQuiet(t, "--all", "--path", path); err == nil {
			t.Errorf("discovery still exposes removed path %q", path)
		}
	}
}

func TestEveryDiscoveredCommandHasParserHelp(t *testing.T) {
	for _, command := range allCommands {
		args := append(strings.Fields(command.Path)[1:], "--help")
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Errorf("%s: %v", command.Path, err)
			continue
		}
		if inv.Command.Path != command.Path || !inv.Help() {
			t.Errorf("%s resolves to %s (help %v)", command.Path, inv.Command.Path, inv.Help())
		}
	}
}

func TestCommandsMarkdownEndsWithOneNewline(t *testing.T) {
	got := renderCommandsMarkdown([]CommandJSON{{
		Path: "sparkwing example", Synopsis: "Example command",
		Examples: []ExampleJSON{{Command: "sparkwing example"}},
	}})
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Fatalf("generated Markdown must end with exactly one newline")
	}
}
