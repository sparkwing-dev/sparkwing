package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypeScriptLintReportsUnsupported(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())
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
}

func TestTypeScriptScaffoldPrintsLockfileSetup(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("PATH", t.TempDir())
	want := "npm --prefix " + shellSingleQuote(filepath.Join(repo, ".sparkwing")) + " install --package-lock-only --ignore-scripts --no-audit --no-fund"
	for _, format := range []string{"plain", "json", "pretty"} {
		var commandErr error
		output := captureStdout(t, func() {
			commandErr = runSparkwing([]string{"pipeline", "new", "--pipeline", "hello-" + format, "--template", "typescript", "-o", format})
		})
		if commandErr != nil {
			t.Fatal(commandErr)
		}
		if format == "json" {
			var report struct {
				SetupCommand string `json:"setup_command"`
			}
			if err := json.Unmarshal([]byte(output), &report); err != nil {
				t.Fatal(err)
			}
			if report.SetupCommand != want {
				t.Fatalf("setup command = %q, want %q", report.SetupCommand, want)
			}
		} else if !strings.Contains(output, want) {
			t.Fatalf("output = %q, want command %q", output, want)
		}
	}
}
