//go:build unix && rewrite_integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRunEvaluatesTheSameGeneratorsAsARunnerHost(t *testing.T) {
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo := runAnywhereRepo(t)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	offlineStopDaemon(t, os.Getenv("SPARKWING_HOME"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, cli, "run", "anywhere", "-o=json")
	command.Dir = repo
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "anywhere-generated-ran") || !strings.Contains(string(output), "anywhere-untyped-generated-ran") {
		t.Fatalf("local run: %v\n%s", err, output)
	}
}
