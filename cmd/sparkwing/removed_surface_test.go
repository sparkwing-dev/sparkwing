package main

import (
	"testing"
)

func TestRemovedBounceAndRerun(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"runs", "bounce"}, {"debug", "rerun"}} {
		if err := runSparkwing(args); err == nil {
			t.Fatalf("removed command executed: %v", args)
		}
		for _, command := range allCommands {
			if command.Path == "sparkwing "+args[0]+" "+args[1] {
				t.Fatalf("removed command remains in help: %s", command.Path)
			}
		}
	}
}
