package main

import (
	"strings"
	"testing"
)

func TestRunMarkdownProvidesBurstOverflowSection(t *testing.T) {
	for _, command := range allCommands {
		if command.Path != "sparkwing run" {
			continue
		}
		pages, err := splitCommandsMarkdown([]CommandJSON{toCommandJSON(command)})
		if err != nil {
			t.Fatal(err)
		}
		page := pages["cli-run.md"]
		for _, policy := range []string{"machine run.routing", "may only restrict routing to local", "Other team runners cannot claim burst jobs"} {
			if !strings.Contains(page, policy) {
				t.Fatalf("run reference omits authorization rule %q", policy)
			}
		}
		for _, line := range strings.Split(page, "\n") {
			if title, heading := strings.CutPrefix(line, "### "); heading && title == "Burst and overflow" {
				return
			}
		}
		t.Fatal("run reference has no burst-and-overflow section anchor")
	}
	t.Fatal("run command is absent")
}
