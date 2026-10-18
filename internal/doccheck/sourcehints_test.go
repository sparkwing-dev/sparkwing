package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositorySourceHintsUseCurrentGrammar(t *testing.T) {
	if !checkSourceHints(filepath.Join("..", "..")) {
		t.Fatal("Go source prints commands outside the current grammar")
	}
}

func TestCheckSourceHintsRejectsStaleHints(t *testing.T) {
	for _, literal := range []string{
		`"  upgrade:  sparkwing update --cli"`,
		`"install the matching CLI: sparkwing update --cli --version %s"`,
		"\"no repos registered; run `sparkwing pipeline add <dir>` first\"",
		`"(sparkwing secrets set --name KEY --file <path>)"`,
	} {
		root := t.TempDir()
		writeSource(t, root, "cmd/tool/hint.go", "package main\n\nvar hint = "+literal+"\n")
		if checkSourceHints(root) {
			t.Fatalf("stale hint accepted: %s", literal)
		}
	}
}

func TestCheckSourceHintsAcceptsCurrentHintsAndProse(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "cmd/tool/hint.go", "package main\n\nvar hints = []string{\n"+
		"\t\"run sparkwing doctor, then sparkwing update --version %s to retry\",\n"+
		"\t\"add one with `sparkwing repos add`\",\n"+
		"\t\"the daemon creates it; sparkwing repos and their SDK pins\",\n"+
		"\t\"sparkwing-dev/sparkwing update --cli\",\n"+
		"}\n")
	writeSource(t, root, "cmd/tool/hint_test.go", "package main\n\nvar old = \"sparkwing update --cli\"\n")
	writeSource(t, root, "internal/x/testdata/hint.go", "package x\n\nvar old = \"sparkwing update --cli\"\n")
	if !checkSourceHints(root) {
		t.Fatal("current hints or prose rejected")
	}
}

func writeSource(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
