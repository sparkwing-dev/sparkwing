package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredDashboardNounCannotPerformServiceActions(t *testing.T) {
	for _, args := range [][]string{{"dashboard"}, {"dashboard", "start", "--addr", "127.0.0.1:0"}, {"dashboard", "status"}, {"dashboard", "kill"}, {"dashboard", "stop"}, {"dashboard", "--help"}, {"-o", "json", "dashboard", "start"}, {"--output=plain", "dashboard", "status"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := outputContractCommand(t, args...)
			var errs bytes.Buffer
			cmd.Stderr = &errs
			out, err := cmd.Output()
			if err == nil || len(out) != 0 || !strings.Contains(errs.String(), "unknown subcommand") {
				t.Fatalf("retired noun: %v stdout=%q stderr=%q", err, out, errs.String())
			}
			assertNoServiceState(t, filepath.Join(cmd.Dir, "state"))
		})
	}
	for _, verb := range []string{"help", "--help", "-h"} {
		t.Run(verb+" dashboard", func(t *testing.T) {
			retired := outputContractCommand(t, verb, "dashboard")
			retiredOut, err := retired.Output()
			if err != nil {
				t.Fatalf("retired help topic: %v stdout=%q", err, retiredOut)
			}
			assertNoServiceState(t, filepath.Join(retired.Dir, "state"))
			invented := outputContractCommand(t, verb, "nosuchnoun")
			inventedOut, err := invented.Output()
			if err != nil {
				t.Fatalf("invented help topic: %v stdout=%q", err, inventedOut)
			}
			if !bytes.Equal(retiredOut, inventedOut) {
				t.Fatalf("retired noun still has help handling of its own:\n%s\n---\n%s", retiredOut, inventedOut)
			}
		})
	}
}

// safety: an unsupported noun reaches the version-transition marker like any
// other, so the home existing is not the signal; a PID, a log, or a record is.
func assertNoServiceState(t *testing.T, home string) {
	t.Helper()
	for _, name := range []string{dashboardPIDFile, dashboardLogFile, "dashboard.lock"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("service state %s exists: %v", name, err)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read service home: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "dashboard-state") {
			t.Fatalf("service record %s exists", entry.Name())
		}
	}
}

func TestServeHelpAndCommandIndexReplaceDashboard(t *testing.T) {
	for _, args := range [][]string{{"serve", "--help", "-o", "json"}, {"serve", "start", "--help", "-o", "json"}, {"-o", "json", "help", "serve", "status"}} {
		cmd := outputContractCommand(t, args...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("serve help: %v %s", err, out)
		}
		records := decodeOutputRecords(t, out)
		if len(records) != 1 || records[0]["kind"] != "help" || !strings.HasPrefix(records[0]["path"].(string), "sparkwing serve") {
			t.Fatalf("wrong help route: %s", out)
		}
		if strings.Contains(string(out), "sparkwing dashboard") {
			t.Fatalf("retired command advertised in help: %s", out)
		}
	}
	cmd := outputContractCommand(t, "commands", "--path", "serve", "--limit", "0", "-o", "json")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("serve command index: %v %s", err, out)
	}
	seen := map[string]bool{}
	for _, record := range decodeOutputRecords(t, out) {
		if path, ok := record["path"].(string); ok {
			seen[path] = true
		}
	}
	for _, path := range []string{"sparkwing serve", "sparkwing serve start", "sparkwing serve status", "sparkwing serve stop", "sparkwing serve restart", "sparkwing serve logs"} {
		if !seen[path] {
			t.Fatalf("index omits %s: %s", path, out)
		}
	}
	for _, command := range allCommands {
		if command.Path == "sparkwing dashboard" || strings.HasPrefix(command.Path, "sparkwing dashboard ") {
			t.Fatalf("retired route remains registered: %s", command.Path)
		}
	}
	search := outputContractCommand(t, "commands", "--query", "dashboard", "-o", "json")
	result, err := search.Output()
	if err != nil {
		t.Fatalf("dashboard keyword search: %v %s", err, result)
	}
	matched := false
	for _, record := range decodeOutputRecords(t, result) {
		if record["path"] == "sparkwing serve" {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("dashboard product term no longer finds serve: %s", result)
	}
}

func TestServeCompletionReplacesDashboardRoot(t *testing.T) {
	found := false
	for _, command := range topLevelSubcommands() {
		if command.Name == "dashboard" {
			t.Fatal("completion retains retired root")
		}
		if command.Name == "serve" {
			found = true
		}
	}
	if !found {
		t.Fatal("completion omits serve")
	}
	cmd := outputContractCommand(t, "_complete-verbs")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("completion helper: %v %s", err, out)
	}
	if !strings.Contains(string(out), "serve\t") || strings.Contains(string(out), "dashboard\t") {
		t.Fatalf("wrong root completion: %s", out)
	}
	if !strings.Contains(renderBash(), "_complete-verbs") || !strings.Contains(renderZsh(), "_complete-verbs") || !strings.Contains(renderFish(), "serve") {
		t.Fatal("shell completions omit the current command registry")
	}
}
