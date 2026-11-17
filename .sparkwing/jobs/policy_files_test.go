package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyGofmtJudgesOnlyTrackedFiles(t *testing.T) {
	for _, mode := range []string{"ignored", "untracked", "tracked"} {
		t.Run(mode, func(t *testing.T) {
			tracked := mode == "tracked"
			root := gateFixtureRepo(t)
			writeGoFile(t, filepath.Join(root, ".gitignore"), ".Codex-scratch/\n")
			gitAddAll(t, root)
			name := ".Codex-scratch/update-drain/update.go"
			if mode != "ignored" {
				name = "internal/update.go"
			}
			writeGoFile(t, filepath.Join(root, name), "package internal\n\nfunc  Update( ) {}\n")
			if tracked {
				gitAddAll(t, root)
			}
			err := runGofmt(newTestContext(t))
			if tracked {
				if err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("tracked malformed file: %v", err)
				}
			} else if err != nil {
				t.Fatalf("ignored malformed file affected policy: %v", err)
			}
		})
	}
}

func TestMarkdownlintJudgesOnlyTrackedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes pinned markdownlint")
	}
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, ".markdownlint-cli2.yaml"), "globs: ['docs/**/*.md', '*.md']\n")
	writeGoFile(t, filepath.Join(root, ".gitignore"), "docs/scratch/\n")
	gitAddAll(t, root)
	name := "docs/scratch/bad.md"
	writeGoFile(t, filepath.Join(root, name), "# Bad\n# Bad\n")
	if err := runMarkdownlint(newTestContext(t)); err != nil {
		t.Fatalf("ignored markdown affected policy: %v", err)
	}
	runTestGit(t, root, "add", "-f", name)
	if err := runMarkdownlint(newTestContext(t)); err == nil {
		t.Fatal("tracked malformed markdown passed")
	}
}

func TestActionlintJudgesOnlyTrackedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes pinned actionlint")
	}
	root := gateFixtureRepo(t)
	name := ".github/workflows/scratch.yaml"
	writeGoFile(t, filepath.Join(root, ".gitignore"), name+"\n")
	gitAddAll(t, root)
	writeGoFile(t, filepath.Join(root, name), "invalid: [\n")
	if err := runActionlint(newTestContext(t)); err != nil {
		t.Fatalf("ignored workflow affected lint: %v", err)
	}
	runTestGit(t, root, "add", "-f", name)
	if err := runActionlint(newTestContext(t)); err == nil {
		t.Fatal("tracked malformed workflow passed")
	}
}

func TestTrackedPolicyChangelogIgnoresScratch(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n")
	writeGoFile(t, filepath.Join(root, ".gitignore"), "docs/changes/scratch.md\n")
	gitAddAll(t, root)
	name := "docs/changes/scratch.md"
	writeGoFile(t, filepath.Join(root, name), "invalid fragment\n")
	if err := checkTrackedChangelogLinks(newTestContext(t)); err != nil {
		t.Fatalf("ignored fragment affected policy: %v", err)
	}
	runTestGit(t, root, "add", "-f", name)
	if err := checkTrackedChangelogLinks(newTestContext(t)); err == nil {
		t.Fatal("tracked malformed fragment passed policy")
	}
}

func TestGeneratedPagesJudgeOnlyTrackedDocs(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(sourceTreeRoot(), "bin/check-generated-pages.sh"))
	if err != nil {
		t.Fatal(err)
	}
	root := gateFixtureRepo(t)
	generated := t.TempDir()
	writeGoFile(t, filepath.Join(root, "bin/check-generated-pages.sh"), string(script))
	writeGoFile(t, filepath.Join(root, "docs/cli-main.md"), "GENERATED\n")
	writeGoFile(t, filepath.Join(generated, "cli-main.md"), "GENERATED\n")
	writeGoFile(t, filepath.Join(root, ".gitignore"), "docs/cli-scratch.md\n")
	gitAddAll(t, root)
	writeGoFile(t, filepath.Join(root, "docs/cli-scratch.md"), "GENERATED\n")
	check := preReleaseShell("bash bin/check-generated-pages.sh " + shellQuote(generated) + " 'cli-*.md' GENERATED regenerate")
	if err := check(newTestContext(t)); err != nil {
		t.Fatalf("ignored generated page affected policy: %v", err)
	}
	runTestGit(t, root, "add", "-f", "docs/cli-scratch.md")
	if err := check(newTestContext(t)); err == nil {
		t.Fatal("tracked stale generated page passed policy")
	}
}
