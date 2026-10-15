package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootCheckoutValidationPrecedesChdir(t *testing.T) {
	start := t.TempDir()
	t.Chdir(start)
	missing := filepath.Join(start, "missing")
	if err := runSparkwing([]string{"--checkout", missing, "run", "build", "--unknown"}); exitCodeFor(err) != 2 {
		t.Fatalf("got %v", err)
	}
	if err := runSparkwing([]string{"--checkout", missing, "version", "--offline"}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("got %v", err)
	}
	got, err := os.Getwd()
	if err != nil || got != start {
		t.Fatalf("working directory changed: %s %v", got, err)
	}
}

func TestRootCheckoutAppliesInEitherPositionAndRestores(t *testing.T) {
	start := t.TempDir()
	t.Chdir(start)
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", project, "version", "--offline", "-o", "json"},
		{"version", "--checkout", project, "--offline", "-o", "json"},
	} {
		if err := runSparkwing(args); err != nil {
			t.Fatal(err)
		}
		got, err := os.Getwd()
		if err != nil || got != start {
			t.Fatalf("working directory not restored: %s %v", got, err)
		}
	}
}
