package main

import (
	"strings"
	"testing"
)

func TestZshCompletionDoesNotRequestRemovedRunnerSelection(t *testing.T) {
	script := renderZsh()
	for _, stale := range []string{"--default-runner", "_complete-runners", "default_runner"} {
		if strings.Contains(script, stale) {
			t.Errorf("zsh completion contains removed runner selection %q", stale)
		}
	}
}
