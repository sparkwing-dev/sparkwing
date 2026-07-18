package main

import (
	"os"
	"path/filepath"
	"testing"
)

const fakeRegistry = `package main

var cmdRoot = Command{
	Path: "sparkwing",
	Subcommands: []SubcommandRef{
		{"pipeline", "This repo's pipelines"},
		{"run", "Run a pipeline"},
		{"configure", "Laptop-local config"},
	},
}

var cmdRun = Command{
	Path: "sparkwing run",
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name", Required: true},
	},
}

var cmdRunConfig = Command{Path: "sparkwing run config"}

var cmdPipeline = Command{
	Path: "sparkwing pipeline",
	Subcommands: []SubcommandRef{
		{"list", "List pipelines"},
		{"hooks", "Git hooks"},
	},
}

var cmdPipelineList = Command{Path: "sparkwing pipeline list"}
var cmdPipelineHooks = Command{Path: "sparkwing pipeline hooks"}
var cmdPipelineHooksInstall = Command{Path: "sparkwing pipeline hooks install"}

var cmdConfigure = Command{
	Path: "sparkwing configure",
	Subcommands: []SubcommandRef{
		{"xrepo", "Cross-repo registry: list / add / remove"},
	},
}
`

func writeFakeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "cmd", "sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "help_registry.go"), []byte(fakeRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadRegistry_CollectsPathsSubcommandsAndHiddenVerbs(t *testing.T) {
	root := writeFakeRepo(t)
	valid, posArgs, err := loadRegistry(root)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	wantValid := []string{
		"sparkwing pipeline list",
		"sparkwing pipeline hooks",
		"sparkwing configure xrepo",
		"sparkwing run-node",
		"sparkwing handle-trigger",
	}
	for _, p := range wantValid {
		if !valid[p] {
			t.Errorf("valid set missing %q", p)
		}
	}
	if !posArgs["sparkwing run"] {
		t.Errorf("run should be recorded as accepting a positional argument")
	}
	if posArgs["sparkwing pipeline"] {
		t.Errorf("pipeline should not be recorded as accepting a positional argument")
	}
}

func TestResolvePath(t *testing.T) {
	root := writeFakeRepo(t)
	valid, posArgs, err := loadRegistry(root)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	cases := []struct {
		name    string
		tokens  []string
		wantBad string
	}{
		{"valid deep path", []string{"pipeline", "hooks", "install"}, ""},
		{"valid group only", []string{"pipeline"}, ""},
		{"unknown subcommand under group", []string{"pipeline", "sparkz"}, "sparkz"},
		{"unknown top-level verb", []string{"nope"}, "nope"},
		{"positional after posargs command", []string{"run", "my-pipeline"}, ""},
		{"subcommand still wins over positional", []string{"run", "config"}, ""},
		{"positional after leaf command", []string{"pipeline", "list", "extra"}, ""},
		{"flag ends the walk", []string{"pipeline", "--help"}, ""},
		{"placeholder ends the walk", []string{"configure", "xrepo", "add"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolvePath(c.tokens, valid, posArgs); got != c.wantBad {
				t.Errorf("resolvePath(%v) = %q, want %q", c.tokens, got, c.wantBad)
			}
		})
	}
}

func TestExtractInvocations_OnlyRealCommands(t *testing.T) {
	doc := "# Title\n" +
		"Prose mentioning sparkwing pipeline in a sentence.\n" +
		"Inline `sparkwing pipeline list` here.\n" +
		"```\n" +
		"sparkwing pipeline trigger build --profile x\n" +
		"  1. sparkwing resolves the profile\n" +
		"```\n" +
		"```go\n" +
		"sw.Run(ctx)\n" +
		"```\n"
	got := extractInvocations("d.md", doc)
	if len(got) != 2 {
		t.Fatalf("got %d invocations, want 2: %+v", len(got), got)
	}
	if got[0].raw != "sparkwing pipeline list" {
		t.Errorf("first invocation = %q", got[0].raw)
	}
	if got[1].raw != "sparkwing pipeline trigger build --profile x" {
		t.Errorf("second invocation = %q", got[1].raw)
	}
}

func writeDoc(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCLIVerbs_FailsOnRenamedVerb(t *testing.T) {
	root := writeFakeRepo(t)
	content := t.TempDir()
	writeDoc(t, content, "bad.md", "```bash\nsparkwing pipeline frobnicate\n```\n")
	if checkCLIVerbs(content, root) {
		t.Fatal("expected failure on nonexistent verb `pipeline frobnicate`")
	}
}

func TestCheckCLIVerbs_PassesAndSkipsExemptDocs(t *testing.T) {
	root := writeFakeRepo(t)
	content := t.TempDir()
	writeDoc(t, content, "good.md", "```bash\nsparkwing pipeline list\nsparkwing run my-pipeline --sw-dry-run\n```\n")
	writeDoc(t, content, "cli-reference.md", "```bash\nsparkwing totally-made-up\n```\n")
	writeDoc(t, content, "mcp.md", "> STATUS: design / not yet shipped.\n```bash\nsparkwing mcp serve\n```\n")
	if !checkCLIVerbs(content, root) {
		t.Fatal("expected pass: real commands resolve, generated + unshipped docs are skipped")
	}
}
