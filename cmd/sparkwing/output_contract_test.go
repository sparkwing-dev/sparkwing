package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOutputContractProcess(t *testing.T) {
	if os.Getenv("SPARKWING_OUTPUT_CONTRACT_HELPER") != "1" {
		return
	}
	for index, argument := range os.Args {
		if argument == "--" {
			if err := runSparkwing(os.Args[index+1:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(exitCodeFor(err))
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func outputContractCommand(t *testing.T, arguments ...string) *exec.Cmd {
	t.Helper()
	commandContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	home := t.TempDir()
	environment := append(os.Environ(), "SPARKWING_OUTPUT_CONTRACT_HELPER=1", "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"APPDATA="+filepath.Join(home, "config"), "TEST_TELEMETRY_DIR=",
		"SPARKWING_HOME="+filepath.Join(home, "state"), "SPARKWING_PROFILE=", "SPARKWING_CONTROLLER=", "NO_COLOR=1", "MSYSTEM=MINGW64", "TERM_PROGRAM=mintty", "TERM=xterm-256color")
	// SAFETY: Go's telemetry sidecar can outlive the CLI and write during temporary-home cleanup.
	telemetry := exec.CommandContext(commandContext, "go", "telemetry", "off")
	telemetry.Dir = home
	telemetry.Env = environment
	if output, err := telemetry.CombinedOutput(); err != nil {
		t.Fatalf("disable Go telemetry in test home: %v: %s", err, output)
	}
	command := exec.CommandContext(commandContext, os.Args[0], append([]string{"-test.run=^TestOutputContractProcess$", "--"}, arguments...)...)
	command.Dir = home
	command.Env = environment
	return command
}

func decodeOutputRecords(t *testing.T, output []byte) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil || record == nil {
			t.Fatalf("expected one complete object per line, got %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestOutputContractPipeRoutes(t *testing.T) {
	for _, arguments := range [][]string{
		{},
		{"--output", "json"},
		{"commands"},
		{"version", "--offline"},
		{"info"},
		{"info", "--for-agent"},
		{"info", "--first-time"},
		{"--output=json", "version", "--offline"},
		{"profile"},
		{"docs", "list"},
		{"docs", "guides"},
		{"docs", "versions"},
		{"docs", "all"},
		{"docs", "read", "--topic", "getting-started"},
		{"docs", "migrations", "read", "--version", "v0.37.3"},
		{"--help"},
		{"docs", "--help"},
		{"pipeline", "list", "--help"},
		{"completion", "--shell", "bash"},
		{"cache", "info"},
		{"daemon", "status"},
		{"queue"},
	} {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			command := outputContractCommand(t, arguments...)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			output, err := command.Output()
			if err != nil {
				t.Fatalf("command: %v: %s", err, &stderr)
			}
			if len(decodeOutputRecords(t, output)) == 0 {
				t.Fatal("expected at least one record")
			}
		})
	}
}

func TestOutputContractOverridesAndErrors(t *testing.T) {
	for _, flag := range [][]string{{"-o", "json"}, {"-o=json"}, {"--output", "json"}, {"--output=json"}} {
		command := outputContractCommand(t, append([]string{"version", "--offline"}, flag...)...)
		output, err := command.Output()
		if err != nil || len(decodeOutputRecords(t, output)) != 1 {
			t.Fatalf("%v: %v: %s", flag, err, output)
		}
	}
	for _, flag := range [][]string{{"-o"}, {"--output"}, {"--output="}, {"-o="}, {"-o", ""}, {"-o", "yaml"}, {"-o", "table"}, {"-o", "JSON"}} {
		command := outputContractCommand(t, append([]string{"version", "--offline"}, flag...)...)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if err == nil || len(output) != 0 || !strings.Contains(stderr.String(), "pretty|json|plain") {
			t.Fatalf("%v: err=%v stdout=%q stderr=%q", flag, err, output, &stderr)
		}
	}
	for _, mode := range []string{"pretty", "plain"} {
		command := outputContractCommand(t, "version", "--offline", "--output", mode)
		output, err := command.Output()
		if err != nil || len(output) == 0 || json.Valid(output) {
			t.Fatalf("explicit %s: %v: %s", mode, err, output)
		}
	}
}

func TestOutputContractPlainCompletionAndEmptyList(t *testing.T) {
	command := outputContractCommand(t, "completion", "--shell", "bash", "--output", "plain")
	output, err := command.Output()
	if err != nil || !strings.Contains(string(output), "complete ") || json.Valid(output) {
		t.Fatalf("plain completion: %v: %s", err, output)
	}
	command = outputContractCommand(t, "configure", "profiles", "list")
	output, err = command.Output()
	if err != nil || len(output) != 0 {
		t.Fatalf("empty profile listing: %v: %s", err, output)
	}
}

func TestOutputContractKeepsFlagValues(t *testing.T) {
	for _, query := range []string{"-output", "help", "--help"} {
		command := outputContractCommand(t, "docs", "search", "-q", query, "-o", "json")
		output, err := command.Output()
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		for _, record := range decodeOutputRecords(t, output) {
			if record["kind"] == "help" {
				t.Fatalf("query %q was treated as help", query)
			}
		}
	}
}

func TestRootOutputStaysBeforeChildBoundary(t *testing.T) {
	arguments := moveRootOutput([]string{"-o", "pretty", "run", "pipeline", "--", "--output", "child"})
	expected := []string{"run", "pipeline", "-o", "pretty", "--", "--output", "child"}
	if !slices.Equal(arguments, expected) {
		t.Fatalf("got %q, want %q", arguments, expected)
	}
}
