package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypeScriptLintReportsUnsupported(t *testing.T) {
	t.Chdir(t.TempDir())
	fakeNPM(t, 0)
	var err error
	captureStdout(t, func() {
		err = runSparkwing([]string{"pipeline", "new", "--pipeline", "hello", "--template", "typescript", "-o", "json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"plain", "json"} {
		output := captureStdout(t, func() { err = runSparkwing([]string{"pipeline", "lint", "--pipeline", "hello", "-o", format}) })
		if err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var report struct {
				Status string `json:"status"`
				Notice string `json:"notice"`
			}
			if err := json.Unmarshal([]byte(output), &report); err != nil {
				t.Fatal(err)
			}
			if report.Status != "unsupported" || !strings.Contains(report.Notice, "TypeScript pipelines aren't linted yet") {
				t.Fatalf("report = %+v", report)
			}
		} else if !strings.Contains(output, "TypeScript pipelines aren't linted yet") {
			t.Fatalf("output = %q", output)
		}
	}
	captureStdout(t, func() { err = runSparkwing([]string{"pipeline", "lint", "--pipeline", "missing", "-o", "json"}) })
	if err == nil || !strings.Contains(err.Error(), "no pipeline named") {
		t.Fatalf("unknown pipeline error = %v", err)
	}
}

func TestTypeScriptScaffoldWritesLockfile(t *testing.T) {
	for _, format := range []string{"plain", "json", "pretty"} {
		t.Run(format, func(t *testing.T) {
			repo := t.TempDir()
			t.Chdir(repo)
			calls := fakeNPM(t, 0)
			var commandErr error
			output := captureStdout(t, func() {
				commandErr = runSparkwing([]string{"pipeline", "new", "--pipeline", "hello", "--template", "typescript", "-o", format})
			})
			if commandErr != nil {
				t.Fatal(commandErr)
			}
			dir := filepath.Join(repo, ".sparkwing")
			physical, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			recorded, err := os.ReadFile(calls)
			if err != nil {
				t.Fatalf("scaffold never ran npm: %v", err)
			}
			want := physical + "|install --package-lock-only --ignore-scripts --no-audit --no-fund\n"
			if string(recorded) != want {
				t.Fatalf("npm calls = %q, want %q", recorded, want)
			}
			if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err != nil {
				t.Fatalf("lockfile: %v", err)
			}
			if strings.Contains(output, "package-lock-only") || strings.Contains(output, "setup_command") {
				t.Fatalf("scaffold still asks for a manual npm step: %q", output)
			}
		})
	}
}

func TestTypeScriptScaffoldFailsWithTheNPMCommand(t *testing.T) {
	for name, setPath := range map[string]func(*testing.T){
		"npm missing": func(t *testing.T) { t.Setenv("PATH", t.TempDir()) },
		"npm fails": func(t *testing.T) {
			t.Setenv("HTTPS_PROXY", "http://ci:proxy-pass-7f3a9c@proxy.internal:3128")
			fakeNPM(t, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			t.Chdir(repo)
			setPath(t)
			var commandErr error
			captureStdout(t, func() {
				commandErr = runSparkwing([]string{"pipeline", "new", "--pipeline", "hello", "--template", "typescript", "-o", "json"})
			})
			dir := filepath.Join(repo, ".sparkwing")
			command := "npm --prefix " + shellSingleQuote(dir) + " install --package-lock-only --ignore-scripts --no-audit --no-fund"
			if commandErr == nil || !strings.Contains(commandErr.Error(), command) {
				t.Fatalf("error = %v, want it to name %q", commandErr, command)
			}
			if name == "npm fails" && !strings.Contains(commandErr.Error(), "network request failed") {
				t.Fatalf("error = %v, want npm's output", commandErr)
			}
			if strings.Contains(commandErr.Error(), "proxy-pass-7f3a9c") {
				t.Fatalf("error = %v, exposes the proxy credential", commandErr)
			}
			if _, err := os.Stat(filepath.Join(dir, "jobs", "hello.ts")); err != nil {
				t.Fatalf("scaffold files were not kept: %v", err)
			}
		})
	}
}
