package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeltaSweepAgentInfoPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain ESC")
	}
	root := filepath.Join(t.TempDir(), "repo\x1b[2J")
	if err := os.MkdirAll(filepath.Join(root, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte("pipelines: [invalid YAML\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, args := range [][]string{{"-o", "pretty"}, {"--for-agent", "-o", "pretty"}, {"--for-agent", "-o", "plain"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var err error
			got := captureStdout(t, func() { err = runInfoArgs(args) })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "pipelines unavailable") {
				t.Fatalf("fixture did not reach project summary: %q", got)
			}
			if strings.ContainsRune(got, '\x1b') {
				t.Fatalf("active ESC in dynamic info summary: %q", got)
			}
			if !strings.Contains(got, `\x1b[2J`) {
				t.Fatalf("path escape missing: %q", got)
			}
		})
	}
	for _, args := range [][]string{{"-o", "json"}, {"--for-agent", "-o", "json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var err error
			got := captureStdout(t, func() { err = runInfoArgs(args) })
			if err != nil {
				t.Fatal(err)
			}
			if args[0] == "--for-agent" {
				var record textRecord
				if err := json.Unmarshal([]byte(got), &record); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(record.Text, filepath.Join(root, ".sparkwing")) {
					t.Fatalf("agent JSON path changed: %q", record.Text)
				}
			} else {
				var info Info
				if err := json.Unmarshal([]byte(got), &info); err != nil {
					t.Fatal(err)
				}
				if info.Project.SparkwingDir != filepath.Join(root, ".sparkwing") {
					t.Fatalf("JSON path changed: %q", info.Project.SparkwingDir)
				}
			}
		})
	}
}

func TestDeltaSweepReferenceReceipt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain ESC")
	}
	dir := filepath.Join(t.TempDir(), "out\x1b[2J")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cli-stale.md"), []byte("<!-- GENERATED from the CLI command registry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var err error
	var diagnostics string
	out := captureStdout(t, func() {
		diagnostics = captureStderr(t, func() {
			err = writeSplitMarkdownOutput(dir, []CommandJSON{{Path: "sparkwing", Synopsis: "root"}}, "pretty")
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out+diagnostics, '\x1b') {
		t.Fatalf("active ESC in reference receipts stdout=%q stderr=%q", out, diagnostics)
	}
	if !strings.Contains(out, `\x1b[2J`) || !strings.Contains(diagnostics, `\x1b[2J`) {
		t.Fatalf("path escape missing: stdout=%q stderr=%q", out, diagnostics)
	}
	if _, err := os.Stat(filepath.Join(dir, "cli-stale.md")); !os.IsNotExist(err) {
		t.Fatalf("original stale page was not removed: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no reference pages written to original directory")
	}
	got := captureStdout(t, func() {
		err = writeSplitMarkdownOutput(dir, []CommandJSON{{Path: "sparkwing", Synopsis: "root"}}, "json")
	})
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal([]byte(got), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Directory != dir {
		t.Fatalf("JSON directory changed: %q", receipt.Directory)
	}
}

func TestDeltaSweepHookUninstallReceipt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain ESC")
	}
	root := t.TempDir()
	hooks := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit\x1b[2J"), []byte(sparkwingHookMarker), 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	got := captureStderr(t, func() { err = uninstallHooks(nil, root) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("active ESC in hook removal receipt: %q", got)
	}
	if !strings.Contains(got, `\x1b[2J`) {
		t.Fatalf("hook escape missing: %q", got)
	}
	if _, err := os.Stat(filepath.Join(hooks, "pre-commit\x1b[2J")); !os.IsNotExist(err) {
		t.Fatalf("original hook was not removed: %v", err)
	}
}
