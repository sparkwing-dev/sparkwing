package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func publicCommandReference(t *testing.T) map[string]string {
	t.Helper()
	commands := make([]CommandJSON, 0, len(allCommands))
	for _, command := range allCommands {
		if !command.Hidden {
			commands = append(commands, toCommandJSON(command))
		}
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Path < commands[j].Path })
	pages, err := splitCommandsMarkdown(commands)
	if err != nil {
		t.Fatal(err)
	}
	return pages
}

func TestCommandReferenceMatchesRegistry(t *testing.T) {
	for name, want := range publicCommandReference(t) {
		got, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("docs/%s differs from the command registry; regenerate with bin/gen-cli-docs.sh", name)
		}
	}
}
