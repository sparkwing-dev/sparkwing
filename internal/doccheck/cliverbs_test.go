package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestRepositoryCLIExamplesUseCurrentGrammar(t *testing.T) {
	if !checkCLIVerbs(filepath.Join("..", "..", "docs")) {
		t.Fatal("documentation invokes commands outside the current grammar")
	}
}

func TestExistingCIExamplesUseCurrentGrammar(t *testing.T) {
	blocks, err := extract(filepath.Join("..", "..", "docs"), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	commands := 0
	var inspect func(*yaml.Node)
	inspect = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode {
			for _, line := range strings.Split(node.Value, "\n") {
				args := strings.Fields(line)
				if len(args) == 0 || args[0] != "sparkwing" {
					continue
				}
				commands++
				if _, err := clisurface.Parse("sparkwing", args[1:]); err != nil {
					t.Errorf("CI example %q: %v", line, err)
				}
			}
		}
		for _, child := range node.Content {
			inspect(child)
		}
	}
	for _, block := range blocks {
		if block.file != "ci-embedded.md" {
			continue
		}
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(block.body), &doc); err != nil {
			t.Fatal(err)
		}
		inspect(&doc)
	}
	if commands == 0 {
		t.Fatal("CI guide contains no Sparkwing invocations")
	}
}

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

func TestCheckCLIVerbsValidatesFencedGrammar(t *testing.T) {
	for _, command := range []string{
		"run deploy --sw-no-cache",
		"run deploy --region us-east",
		"runs status run-123",
		"--profile prod run deploy",
		"runs logs --run run-123 --limit 0",
	} {
		t.Run(command, func(t *testing.T) {
			content := t.TempDir()
			writeDoc(t, content, "guide.md", "```bash\nsparkwing "+command+"\n```\n")
			if checkCLIVerbs(content) {
				t.Fatalf("invalid grammar accepted: %s", command)
			}
		})
	}
}

func TestCheckCLIVerbsRejectsRetiredInlineFlags(t *testing.T) {
	for _, command := range []string{
		"secrets set --name TYPESAFE_API_KEY --file <path>",
		"examples --name lint-test-go --body",
		"pipeline new --name deploy",
		"run build --sw-no-cache",
		"pipeline list --query build",
		"pipeline plan --static",
		"pipeline list -x",
		"secrets set --name NAME --file\n<path> --pipeline PIPELINE --profile PROFILE",
	} {
		t.Run(command, func(t *testing.T) {
			content := t.TempDir()
			writeDoc(t, content, "guide.md", "Run `sparkwing "+command+"` first.\n")
			if checkCLIVerbs(content) {
				t.Fatalf("retired inline flag accepted: %s", command)
			}
		})
	}
}

func TestCheckCLIVerbsAcceptsInlineFragments(t *testing.T) {
	content := t.TempDir()
	writeDoc(t, content, "guide.md", strings.Join([]string{
		"`sparkwing run`",
		"`sparkwing secrets set --secret NAME --file <path>`",
		"`sparkwing runs status --run`",
		"`sparkwing run --sw-cache-from P`",
		"`sparkwing run build -- --author-option value`",
	}, "\n"))
	if !checkCLIVerbs(content) {
		t.Fatal("valid inline fragments rejected")
	}
}

func TestCheckCLIVerbsShellExamples(t *testing.T) {
	content := t.TempDir()
	writeDoc(t, content, "guide.md", "```bash\n"+
		"sparkwing pipeline trigger deploy --profile prod \\\n"+
		"  --runner here --reason \"deploy from this machine\" -- --region us-east\n"+
		"$ sparkwing runs status --run <id> -o json | jq .status\n"+
		"sparkwing run deploy -- --arbitrary='two words' # author flags\n"+
		"sparkwing profiles add --profile prod --controller https://example.com --token-stdin < token\n"+
		"sparkwing runs logs --run <id> --grep '|' >/tmp/result\n"+
		"sparkwing runs logs --run <id> --grep '>' 2>/tmp/errors\n"+
		"# sparkwing run deploy --obsolete is a shell comment\n"+
		"```\nInline fragments remain valid: `sparkwing runs status`.\n")
	if !checkCLIVerbs(content) {
		t.Fatal("valid shell examples rejected")
	}
}

func TestCheckCLIVerbsRejectsMalformedShellExamples(t *testing.T) {
	for _, command := range []string{
		"sparkwing pipeline trigger deploy --profile prod --runner here --reason 'unfinished",
		"sparkwing run deploy \\",
	} {
		content := t.TempDir()
		writeDoc(t, content, "guide.md", "```sh\n"+command+"\n```\n")
		if checkCLIVerbs(content) {
			t.Fatalf("malformed shell example accepted: %q", command)
		}
	}
}

func TestCheckCLIVerbsReportsReadFailures(t *testing.T) {
	if checkCLIVerbs(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("missing documentation reported as a passing check")
	}
}

func TestMigrationDestinationsUseCurrentGrammar(t *testing.T) {
	for _, example := range []string{"sparkwing run ci --target prod", "sparkwing pipeline hooks status --fleet", "sparkwing pipeline hooks status --all", "sparkwing cluster triggers list"} {
		t.Run(example, func(t *testing.T) {
			content := t.TempDir()
			if err := os.Mkdir(filepath.Join(content, "migrations"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeDoc(t, filepath.Join(content, "migrations"), "v1.md", "| Before | After |\n|---|---|\n| `sparkwing old-command --gone` | `"+example+"` |\n")
			if checkCLIVerbs(content) {
				t.Fatalf("invalid migration destination accepted: %s", example)
			}
		})
	}
	content := t.TempDir()
	if err := os.Mkdir(filepath.Join(content, "migrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, filepath.Join(content, "migrations"), "v1.md", "| Before | After |\n|---|---|\n| `sparkwing old-command --gone` | `sparkwing run ci` |\n\nBefore:\n```bash\nsparkwing old-command --gone\n```\nAfter:\n```bash\nsparkwing pipeline hooks status --all-repos\n```\n")
	if !checkCLIVerbs(content) {
		t.Fatal("historical Before examples were checked")
	}
}

func TestMigrationAfterSectionChecksEveryExample(t *testing.T) {
	content := t.TempDir()
	if err := os.Mkdir(filepath.Join(content, "migrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, filepath.Join(content, "migrations"), "v1.md", "### Before\n```bash\nsparkwing old-command\n```\n### After\n```bash\nsparkwing run ci\n```\n```bash\nsparkwing pipeline hooks status --fleet\n```\n")
	if checkCLIVerbs(content) {
		t.Fatal("second After example was ignored")
	}
}

func TestMigrationCurrentInstructionsAreAllChecked(t *testing.T) {
	for _, doc := range []string{
		"Run `sparkwing crons tick` now.",
		"Before: `sparkwing retired`.\n\nRun `sparkwing crons tick` now.",
		"- **Before:** `sparkwing retired`.\n\nRun `sparkwing crons tick` now.",
		"Before:\n```bash\nsparkwing retired\n```\n\nOperator steps:\nRun `sparkwing crons tick` now.",
		"### Before\n```bash\nsparkwing retired\n```\n### Operator steps\nRun `sparkwing crons tick` now.",
		"| Before | After | Notes |\n|---|---|---|\n| `sparkwing retired` | `sparkwing run ci` | Run `sparkwing crons tick` |\n",
	} {
		content := t.TempDir()
		if err := os.Mkdir(filepath.Join(content, "migrations"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeDoc(t, filepath.Join(content, "migrations"), "v1.md", doc)
		if checkCLIVerbs(content) {
			t.Fatalf("unchecked current instruction: %s", doc)
		}
	}
}

func TestMigrationUnprefixedInstructionsUseFullGrammar(t *testing.T) {
	for _, command := range []string{"cloud status", "cloud status --cluster", "configure profiles show --name prod --show-token", "update --cli", "pipeline hooks status --fleet", "crons tick"} {
		for _, example := range []string{"Run `" + command + "` now.", "Run `" + strings.Replace(command, " ", "\n", 1) + "` now.", "```bash\n" + command + "\n```", "```console\n$\t" + command + "\n```", "| Before | After |\n|---|---|\n| `retired --gone` | `" + command + "` |"} {
			content := t.TempDir()
			if err := os.Mkdir(filepath.Join(content, "migrations"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeDoc(t, filepath.Join(content, "migrations"), "v1.md", example)
			if checkCLIVerbs(content) {
				t.Fatalf("unchecked instruction: %s", example)
			}
		}
	}
}

func TestArchivedMigrationNotesDoNotExemptCurrentInstructions(t *testing.T) {
	content := t.TempDir()
	if err := os.Mkdir(filepath.Join(content, "migrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	archive := "# Migration\n## Before: archived notes\n### Connections\nRun `cloud status --cluster`.\n## Current instructions\n"
	writeDoc(t, filepath.Join(content, "migrations"), "v1.md", archive+"Run `profiles show --profile prod`.\n")
	if !checkCLIVerbs(content) {
		t.Fatal("historical notes invalidated current instructions")
	}
	writeDoc(t, filepath.Join(content, "migrations"), "v1.md", archive+"Run `update --cli`.\n")
	if checkCLIVerbs(content) {
		t.Fatal("archive exempted a current instruction")
	}
}

func TestGuideUnprefixedInstructionsAreChecked(t *testing.T) {
	for _, doc := range []string{"Run `update --cli` now.", "```bash\nsecrets set --name KEY\n```\n", "Run `cloud status`."} {
		content := t.TempDir()
		writeDoc(t, content, "guide.md", doc)
		if checkCLIVerbs(content) {
			t.Fatalf("unchecked instruction: %s", doc)
		}
	}
	content := t.TempDir()
	writeDoc(t, content, "guide.md", "Run `update` or `go run ./cmd/x`.\n")
	if !checkCLIVerbs(content) {
		t.Fatal("current unprefixed instruction rejected")
	}
}
