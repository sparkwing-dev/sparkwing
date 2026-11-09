package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestDocsSidebarGatesPolicyAndReleaseCut(t *testing.T) {
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		job  sparkwing.Workable
	}{
		{"policy", &policyJob{}}, {"release cut", &releaseCutChecksJob{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, "docs"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"go.mod", "go.sum"} {
				body, err := os.ReadFile(filepath.Join(source, name))
				if err != nil {
					t.Fatal(err)
				}
				if name == "go.mod" {
					body = append(body, []byte("\nreplace github.com/sparkwing-dev/sparkwing/sparkwing => ./sparkwing\n")...)
				}
				writeFile(t, filepath.Join(repo, name), string(body))
			}
			for _, name := range []string{"internal", "pkg", "sparkwing", "build"} {
				if err := os.Symlink(filepath.Join(source, name), filepath.Join(repo, name)); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(repo, "go.work"), "go 1.26.0\n\nuse (\n .\n ./sparkwing\n)\n")
			writeFile(t, filepath.Join(repo, "docs", "_sidebar.json"), `{"categories":[{"label":"CLI","slugs":["index"]}]}`)
			writeFile(t, filepath.Join(repo, "docs", "index.md"), "# Docs\n")
			t.Chdir(repo)
			_, _, doc := prepareWork(t, tc.job)
			if findStep(doc, "docs-sidebar") == nil {
				t.Fatal("missing docs-sidebar step")
			}
			if err := runReleaseStateCheck(t, repo, "docs-sidebar"); err != nil {
				t.Fatalf("valid sidebar: %v", err)
			}
			writeFile(t, filepath.Join(repo, "docs", "cli-login.md"), "# Login\n")
			if err := runReleaseStateCheck(t, repo, "docs-sidebar"); err == nil || !strings.Contains(err.Error(), "cli-login.md") {
				t.Fatalf("unlisted page passed or was not named: %v", err)
			}
		})
	}
}
