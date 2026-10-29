package main

import (
	"encoding/json"
	"testing"
)

func TestRunCommandsDispatchThroughFinalNames(t *testing.T) {
	for _, subcommand := range surfaceCommand("sparkwing runs").SubcommandOrder {
		t.Run(subcommand, func(t *testing.T) {
			var commandErr error
			out := captureStdout(t, func() {
				commandErr = runSparkwing([]string{"runs", subcommand, "--help", "-o=json"})
			})
			var help struct {
				Kind string `json:"kind"`
				Path string `json:"path"`
			}
			if commandErr != nil || json.Unmarshal([]byte(out), &help) != nil || help.Kind != "help" || help.Path != "sparkwing runs "+subcommand {
				t.Fatalf("runs command: %v, output=%s", commandErr, out)
			}
			out = captureStdout(t, func() {
				commandErr = runSparkwing([]string{"jobs", subcommand, "--help", "-o=json"})
			})
			if commandErr == nil || exitCodeFor(commandErr) != 2 || out != "" {
				t.Fatalf("retired command: %v, output=%s", commandErr, out)
			}
		})
	}
}
