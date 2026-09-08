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
	for i, arg := range os.Args {
		if arg == "--" {
			if err := runSparkwing(os.Args[i+1:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(exitCodeFor(err))
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func outputContractCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestOutputContractProcess$", "--"}, args...)...)
	home := t.TempDir()
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "SPARKWING_OUTPUT_CONTRACT_HELPER=1", "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"SPARKWING_HOME="+filepath.Join(home, "state"), "SPARKWING_PROFILE=", "SPARKWING_CONTROLLER=", "NO_COLOR=1", "MSYSTEM=MINGW64", "TERM_PROGRAM=mintty", "TERM=xterm-256color")
	return cmd
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
	for _, args := range [][]string{
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
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := outputContractCommand(t, args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("command: %v: %s", err, &stderr)
			}
			if len(decodeOutputRecords(t, out)) == 0 {
				t.Fatal("expected at least one record")
			}
		})
	}
}

func TestOutputContractOverridesAndErrors(t *testing.T) {
	for _, flag := range [][]string{{"-o", "json"}, {"-o=json"}, {"--output", "json"}, {"--output=json"}} {
		cmd := outputContractCommand(t, append([]string{"version", "--offline"}, flag...)...)
		out, err := cmd.Output()
		if err != nil || len(decodeOutputRecords(t, out)) != 1 {
			t.Fatalf("%v: %v: %s", flag, err, out)
		}
	}
	for _, flag := range [][]string{{"-o"}, {"--output"}, {"--output="}, {"-o="}, {"-o", ""}, {"-o", "yaml"}, {"-o", "table"}, {"-o", "JSON"}} {
		cmd := outputContractCommand(t, append([]string{"version", "--offline"}, flag...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil || len(out) != 0 || !strings.Contains(stderr.String(), "pretty|json|plain") {
			t.Fatalf("%v: err=%v stdout=%q stderr=%q", flag, err, out, &stderr)
		}
	}
	for _, mode := range []string{"pretty", "plain"} {
		cmd := outputContractCommand(t, "version", "--offline", "--output", mode)
		out, err := cmd.Output()
		if err != nil || len(out) == 0 || json.Valid(out) {
			t.Fatalf("explicit %s: %v: %s", mode, err, out)
		}
	}
}

func TestOutputContractPlainCompletionAndEmptyList(t *testing.T) {
	cmd := outputContractCommand(t, "completion", "--shell", "bash", "--output", "plain")
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "complete ") || json.Valid(out) {
		t.Fatalf("plain completion: %v: %s", err, out)
	}
	cmd = outputContractCommand(t, "configure", "profiles", "list")
	out, err = cmd.Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("empty profile listing: %v: %s", err, out)
	}
}

func TestOutputContractKeepsFlagValues(t *testing.T) {
	for _, query := range []string{"-output", "help", "--help"} {
		cmd := outputContractCommand(t, "docs", "search", "-q", query, "-o", "json")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		for _, record := range decodeOutputRecords(t, out) {
			if record["kind"] == "help" {
				t.Fatalf("query %q was treated as help", query)
			}
		}
	}
}

func TestRootOutputStaysBeforeChildBoundary(t *testing.T) {
	args := moveRootOutput([]string{"-o", "pretty", "run", "pipeline", "--", "--output", "child"})
	want := []string{"run", "pipeline", "-o", "pretty", "--", "--output", "child"}
	if !slices.Equal(args, want) {
		t.Fatalf("got %q, want %q", args, want)
	}
}
