package main

import (
	"strings"
	"testing"
)

func TestZshCompletionDoesNotRequestRemovedPipelineTargets(t *testing.T) {
	if script := renderZsh(); strings.Contains(script, "_complete-targets") {
		t.Fatal("zsh completion requests targets that the pipeline schema does not declare")
	}
}
