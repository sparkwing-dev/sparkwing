package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseGatesFetchThePreviousTagInTheJobCheckout(t *testing.T) {
	type gate func(context.Context, string, string) error
	for name, gates := range map[string][]gate{
		"wire":   {checkWireBreak},
		"schema": {checkSchemaBreak},
		"both":   {checkWireBreak, checkSchemaBreak},
	} {
		t.Run(name, func(t *testing.T) {
			origin := releaseTagOrigin(t)
			checkout := filepath.Join(t.TempDir(), "checkout")
			gitRun(t, origin, "clone", "--depth=1", "--no-tags", "file://"+origin, checkout)
			if tags := gitRun(t, checkout, "tag", "--list"); tags != "" {
				t.Fatalf("job checkout already has tags: %s", tags)
			}
			before := gitRun(t, checkout, "rev-parse", "HEAD")
			results := make(chan error, len(gates))
			for _, gate := range gates {
				go func() { results <- gate(t.Context(), checkout, "v0.2.0") }()
			}
			for range gates {
				if err := <-results; err != nil {
					t.Error(err)
				}
			}
			gitRun(t, checkout, "cat-file", "-e", "refs/tags/v0.1.0^{commit}")
			if after := gitRun(t, checkout, "rev-parse", "HEAD"); after != before {
				t.Fatal("fetch changed the job's selected commit")
			}
			if status := gitRun(t, checkout, "status", "--porcelain"); status != "" {
				t.Fatalf("fetch changed the working tree: %s", status)
			}
		})
	}
}

func TestReleaseMissingTagExplainsTheJobCheckoutAndRecovery(t *testing.T) {
	origin := releaseTagOrigin(t)
	repo := filepath.Join(t.TempDir(), "checkout")
	gitRun(t, origin, "clone", "--no-tags", origin, repo)
	_, _, err := fileAtTag(t.Context(), repo, "v0.9.0", storeSchemaSourcePath)
	for _, want := range []string{"v0.9.0", "missing in the job checkout", repo, "git -C", "fetch", "origin"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("missing-tag error = %v, want %q", err, want)
		}
	}
}

func releaseTagOrigin(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	for path, body := range map[string]string{
		storeSchemaSourcePath:  "package store\nconst expectedSchemaVersion = 1\n",
		storeSchemaSQLPaths[0]: "CREATE TABLE runs(id TEXT);\n",
		wireSurfaces[0].path:   shapesBefore,
	} {
		path = filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, body)
	}
	commit := func() {
		gitRun(t, repo, "add", ".")
		gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-m", "fixture")
	}
	commit()
	gitRun(t, repo, "update-ref", "refs/tags/v0.1.0", "HEAD")
	writeFile(t, filepath.Join(repo, "README.md"), "After the release.\n")
	commit()
	return repo
}
