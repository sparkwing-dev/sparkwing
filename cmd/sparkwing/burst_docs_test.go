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
		for _, line := range strings.Split(pages["cli-run.md"], "\n") {
			if title, heading := strings.CutPrefix(line, "### "); heading && title == "Burst and overflow" {
				return
			}
		}
		t.Fatal("run reference has no burst-and-overflow section anchor")
	}
	t.Fatal("run command is absent")
}
