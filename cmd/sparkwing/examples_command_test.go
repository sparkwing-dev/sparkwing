package main

import (
	"encoding/json"
	"strings"
	"testing"

	templates "github.com/sparkwing-dev/sparks-core/templates"
)

func TestExamplesCommandUsesDeclaredSelectorAndFormats(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	list, err := templates.List()
	if err != nil || len(list) == 0 {
		t.Fatalf("example catalog: %v, %d entries", err, len(list))
	}
	selected := list[0]
	for _, format := range []string{"pretty", "json", "plain"} {
		t.Run(format, func(t *testing.T) {
			var commandErr error
			output := captureStdout(t, func() {
				commandErr = runSparkwing([]string{"examples", "--example", selected.Manifest.Name, "--body", "-o", format})
			})
			if commandErr != nil || strings.TrimSpace(output) == "" {
				t.Fatalf("example detail: %v, output=%q", commandErr, output)
			}
			if format == "json" {
				var detail templateDetailJSON
				if err := json.Unmarshal([]byte(output), &detail); err != nil || detail.Manifest.Name != selected.Manifest.Name || detail.RenderedBody == "" {
					t.Fatalf("example detail: %v, output=%s", err, output)
				}
			} else if format == "plain" {
				expected, err := renderTemplateWithPlaceholders(selected)
				if err != nil || strings.TrimSpace(output) != strings.TrimSpace(expected) {
					t.Fatalf("plain body differs from example source: %v", err)
				}
			}
		})
	}

	var commandErr error
	output := captureStdout(t, func() {
		commandErr = runSparkwing([]string{"examples", "-o", "plain"})
	})
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != len(list) {
		t.Fatalf("plain catalog: %d lines for %d entries", len(lines), len(list))
	}
	for i, entry := range list {
		if lines[i] != entry.Manifest.Name {
			t.Errorf("plain example %d: %q, want %q", i, lines[i], entry.Manifest.Name)
		}
	}
}

func TestExamplesGuidanceUsesAcceptedCommands(t *testing.T) {
	checkGuidanceCommands(t, captureStdout(t, func() { printTemplateListFooter(1, 1) }))
	checkGuidanceCommands(t, captureStdout(t, func() { printExampleFooter(false) }))
	checkGuidanceCommands(t, captureStdout(t, func() {
		printExampleHits([]exampleHit{{Name: "sample"}}, 1)
	}))
}

func TestExamplesCommandRejectsMissingAndRetiredSelectors(t *testing.T) {
	for _, args := range [][]string{{"examples", "--body"}, {"examples", "--name", "sample"}} {
		var commandErr error
		output := captureStdout(t, func() { commandErr = runSparkwing(args) })
		if commandErr == nil || exitCodeFor(commandErr) != 2 || output != "" {
			t.Errorf("%v: %v, output=%q", args, commandErr, output)
		}
	}
}
