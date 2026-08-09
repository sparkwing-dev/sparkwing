package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRepoFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAuxDocs_CleanPasses(t *testing.T) {
	root := writeFakeRepo(t)
	writeRepoFile(t, root, "README.md", "# Repo\n\n```\nsparkwing pipeline list\n```\n\n[docs](docs/index.md)\n")
	writeRepoFile(t, root, "docs/index.md", "# Docs\n")
	if !checkAuxDocs(root) {
		t.Fatal("clean aux docs must pass")
	}
}

func TestCheckAuxDocs_FailsOnDeadTokenInRootDoc(t *testing.T) {
	root := writeFakeRepo(t)
	writeRepoFile(t, root, "VERSIONING.md", "Config lives in pipelines.yaml.\n")
	if checkAuxDocs(root) {
		t.Fatal("dead token in a root doc must fail")
	}
}

func TestCheckAuxDocs_FailsOnUnresolvedVerbInRootDoc(t *testing.T) {
	root := writeFakeRepo(t)
	writeRepoFile(t, root, "README.md", "```\nsparkwing pipeline sparkz\n```\n")
	if checkAuxDocs(root) {
		t.Fatal("an invocation naming a verb the CLI lacks must fail")
	}
}

func TestCheckAuxDocs_FailsOnBrokenLink(t *testing.T) {
	root := writeFakeRepo(t)
	writeRepoFile(t, root, "README.md", "[gone](docs/never-existed.md)\n")
	if checkAuxDocs(root) {
		t.Fatal("a link to a missing .md must fail")
	}
}

func TestCheckAuxDocs_ScansPipelineHelpStrings(t *testing.T) {
	root := writeFakeRepo(t)
	writeRepoFile(t, root, ".sparkwing/jobs/demo.go", "package jobs\n\nconst help = \"edit pipelines.yaml first\"\n")
	if checkAuxDocs(root) {
		t.Fatal("dead token in a pipeline help string must fail")
	}
}

func TestCheckAuxDocs_ScansExamples(t *testing.T) {
	root := writeFakeRepo(t)
	writeRepoFile(t, root, "examples/ci.yaml", "run: sparkwing run x --mode=ci\n")
	if checkAuxDocs(root) {
		t.Fatal("dead token in an example workflow must fail")
	}
}

func TestCheckAuxDocs_SkipsChangelog(t *testing.T) {
	root := writeFakeRepo(t)
	writeRepoFile(t, root, "CHANGELOG.md", "## v0.1.0\n- Removed pipelines.yaml support.\n")
	if !checkAuxDocs(root) {
		t.Fatal("the changelog is a historical record and must not be scanned")
	}
}

func sidebarJSON(categories, excluded string) string {
	return `{"categories": [` + categories + `], "excluded": [` + excluded + `]}`
}

func TestCheckSidebar_PassesWhenTreeAndSidebarAgree(t *testing.T) {
	content := t.TempDir()
	writeDoc(t, content, "guide.md", "# G\n")
	writeDoc(t, content, "internal-notes.md", "# N\n")
	writeDoc(t, content, "README.md", "# index\n")
	writeRepoFile(t, content, "migrations/v1.md", "# old\n")
	writeDoc(t, content, "_sidebar.json",
		sidebarJSON(`{"label": "Docs", "slugs": ["guide"]}`, `"internal-notes", "migrations/"`))
	if !checkSidebar(content) {
		t.Fatal("listed + excluded pages must pass")
	}
}

func TestCheckSidebar_FailsOnUnlistedPage(t *testing.T) {
	content := t.TempDir()
	writeDoc(t, content, "guide.md", "# G\n")
	writeDoc(t, content, "orphan.md", "# O\n")
	writeDoc(t, content, "_sidebar.json", sidebarJSON(`{"label": "Docs", "slugs": ["guide"]}`, ``))
	if checkSidebar(content) {
		t.Fatal("a page missing from both categories and excluded must fail")
	}
}

func TestCheckSidebar_FailsOnListedButMissingPage(t *testing.T) {
	content := t.TempDir()
	writeDoc(t, content, "_sidebar.json", sidebarJSON(`{"label": "Docs", "slugs": ["ghost"]}`, ``))
	if checkSidebar(content) {
		t.Fatal("a sidebar slug with no page must fail")
	}
}
