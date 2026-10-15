package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistry_CollectsPathsAndHiddenVerbs(t *testing.T) {
	valid, posArgs := loadRegistry()
	wantValid := []string{
		"sparkwing pipeline list",
		"sparkwing pipeline hooks",
		"sparkwing dashboard install",
		"sparkwing __tick",
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
	valid, posArgs := loadRegistry()
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
		{"positional after leaf command", []string{"pipeline", "list", "extra"}, ""},
		{"flag ends the walk", []string{"pipeline", "--help"}, ""},
		{"placeholder ends the walk", []string{"run", "my-pipeline"}, ""},
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
	content := t.TempDir()
	writeDoc(t, content, "bad.md", "```bash\nsparkwing pipeline frobnicate\n```\n")
	if checkCLIVerbs(content) {
		t.Fatal("expected failure on nonexistent verb `pipeline frobnicate`")
	}
}

func TestCheckCLIVerbs_PassesAndSkipsExemptDocs(t *testing.T) {
	content := t.TempDir()
	writeDoc(t, content, "good.md", "```bash\nsparkwing pipeline list\nsparkwing run my-pipeline --sw-dry-run\n```\n")
	writeDoc(t, content, "cli-reference.md", "<!-- GENERATED from the CLI command registry -->\n```bash\nsparkwing totally-made-up\n```\n")
	writeDoc(t, content, "cli-cluster.md", "<!-- GENERATED from the CLI command registry -->\n```bash\nsparkwing also-made-up\n```\n")
	if !checkCLIVerbs(content) {
		t.Fatal("expected pass: real commands resolve, generated docs are skipped")
	}
}

func TestParseInvocation_SkipsRootFlags(t *testing.T) {
	inv, ok := parseInvocation("x.md", 1, "sparkwing -C ~/repo --profile prod pipeline list --all")
	if !ok {
		t.Fatal("not recognized as an invocation")
	}
	if got := strings.Join(inv.tokens, " "); got != "pipeline list --all" {
		t.Errorf("tokens = %q, want the verb path after the root flags", got)
	}
}

func TestCheckCLIVerbsRejectsRemovedCommands(t *testing.T) {
	for _, command := range []string{"cluster tokens create", "serve start", "cloud", "configure", "runs bounce", "debug rerun", "crons tick"} {
		t.Run(command, func(t *testing.T) {
			content := t.TempDir()
			writeDoc(t, content, "guide.md", "`sparkwing "+command+"`\n")
			if checkCLIVerbs(content) {
				t.Fatalf("removed invocation accepted: %s", command)
			}
		})
	}
}
