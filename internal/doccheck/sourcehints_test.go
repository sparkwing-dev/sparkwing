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
		"\"run `sparkwing cluster runners add`\"",
		"\"run `sparkwing unknown-root repair`\"",
		`"run sparkwing cluster object-store reset-breaker --profile NAME"`,
		`"run sparkwing unknown-root repair"`,
		`"Try sparkwing unknown-root repair"`,
		"`Recover the dashboard:\n\n  sparkwing serve start\n\nThen retry.`",
		"`Example:\n  sparkwing retired-root recover\n`",
		"`Example:\n  $ sparkwing retired-root recover\n`",
		"`sparkwing unknown-root repair`",
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
	writeSource(t, root, "cmd/tool/notice.go", "package main\n\nvar notice = \"sparkwing changed %s -> %s: see `sparkwing docs read --topic changelog`\"\n")
	writeSource(t, root, "internal/data/values.go", `package data
var key = []byte("sparkwing signing domain v1")
var message = "sparkwing will build with " + "go1.26"
func data() { run("commit", "-m", "sparkwing sample commit"); strings.NewReader("sparkwing snapshot\n") }
`)
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
