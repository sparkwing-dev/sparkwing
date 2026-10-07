package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoDirectivePolicy(t *testing.T) {
	for _, tc := range []struct {
		version string
		blocked bool
	}{
		{"1.26", false},
		{"1.26.0", false},
		{"1.26.3", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			repo := t.TempDir()
			for _, dir := range []string{".sparkwing", "testdata"} {
				if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(repo, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo "+tc.version+"\n")
			writeFile(t, filepath.Join(repo, ".sparkwing", "go.mod"), "module pipelines\n\ngo 1.26.8\n")
			writeFile(t, filepath.Join(repo, "testdata", "go.mod"), "module fixture\n\ngo 1.26.8\n")
			err := CheckGoDirectives(repo)
			if (err != nil) != tc.blocked {
				t.Fatalf("policy for go %s = %v, want blocked=%v", tc.version, err, tc.blocked)
			}
			if err != nil && (!strings.Contains(err.Error(), "go.mod") || !strings.Contains(err.Error(), tc.version)) {
				t.Fatalf("policy refusal lacks module and version: %v", err)
			}
		})
	}
}

func TestReleaseGoDirectiveCheck(t *testing.T) {
	repo := t.TempDir()
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "go"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(repo, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26\n")
	if err := runReleaseStateCheck(t, repo, "go-directives"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.3\n")
	if err := runReleaseStateCheck(t, repo, "go-directives"); err == nil || !strings.Contains(err.Error(), "1.26.3") {
		t.Fatalf("release accepted patch-level floor: %v", err)
	}
}
