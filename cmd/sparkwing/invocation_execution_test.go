package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/repos"
)

func TestDiscoveryExecutionUsesPublicInvocation(t *testing.T) {
	for _, args := range [][]string{
		{"docs", "list", "--guides", "--help=false"},
		{"docs", "list", "--versions", "--help-all=false"},
		{"docs", "read", "--full", "--help=false"},
		{"docs", "read", "--full", "--help-all=false"},
		{"commands", "--path", "sparkwing docs", "--limit", "2"},
		{"docs", "search", "--search", "pipeline", "--limit", "2"},
		{"version", "--offline"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var commandErr error
			out := captureStdout(t, func() { commandErr = runSparkwing(args) })
			if commandErr != nil {
				t.Fatal(commandErr)
			}
			decoder := json.NewDecoder(strings.NewReader(out))
			count := 0
			for {
				var record map[string]any
				err := decoder.Decode(&record)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("record %d: %v", count, err)
				}
				count++
			}
			if count == 0 {
				t.Fatal("no JSON records")
			}
		})
	}
}

func TestRepositoryCommandsUseCheckoutAndJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "config.yaml"))
	checkout := filepath.Join(home, "repository")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"add", "remove"} {
		var commandErr error
		out := captureStdout(t, func() {
			commandErr = runSparkwing([]string{"repos", verb, "--checkout", checkout})
		})
		if commandErr != nil {
			t.Fatal(commandErr)
		}
		if !json.Valid([]byte(out)) {
			t.Fatalf("%s receipt: %s", verb, out)
		}
		entries, err := repos.List()
		if err != nil {
			t.Fatal(err)
		}
		if verb == "add" && (len(entries) != 1 || entries[0].Path != checkout) || verb == "remove" && len(entries) != 0 {
			t.Fatalf("%s registry: %+v", verb, entries)
		}
	}
}

func TestUtilityMutationsUseSelectedOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SPARKWING_HOME", home)
	library := writeSparkFixture(t, map[string]string{
		"spark.json":     `{"name":"example","description":"fixture","author":"tester","packages":[{"path":"build","description":"build"}]}`,
		"go.mod":         "module example.com/library\n\ngo 1.26.0\n",
		"build/build.go": "package build\n",
	})
	state := filepath.Join(home, "wingd", "state.json")
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("{incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"version", "hold", "--set", "v0.15"},
		{"version", "hold"},
		{"version", "hold", "--clear"},
		{"daemon", "recover-state", "--yes"},
		{"pipeline", "sparks", "lint", "--path", library},
	} {
		var commandErr error
		out := captureStdout(t, func() { commandErr = runSparkwing(args) })
		if commandErr != nil {
			t.Fatalf("%v: %v", args, commandErr)
		}
		if !json.Valid([]byte(out)) {
			t.Fatalf("%v receipt: %s", args, out)
		}
	}
}

func TestUpgradeAppliesRelativeCheckoutOnce(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "checkout", ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "checkout", ".sparkwing", "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)
	t.Setenv("SPARKWING_TOOLCHAIN", "invalid-mode")
	err := runSparkwing([]string{"upgrade", "--checkout", "checkout", "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "unknown toolchain mode") {
		t.Fatalf("selected checkout did not reach engine configuration: %v", err)
	}
}
