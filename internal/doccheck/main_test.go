package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleGoModAbsolutizesRelativeReplacePath(t *testing.T) {
	got, err := moduleGoMod(".")
	if err != nil {
		t.Fatalf("moduleGoMod: %v", err)
	}

	const prefix = "replace github.com/sparkwing-dev/sparkwing => "
	var target string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, prefix) {
			target = strings.TrimPrefix(line, prefix)
		}
	}
	if target == "" {
		t.Fatalf("no replace directive in:\n%s", got)
	}
	if !filepath.IsAbs(target) {
		t.Fatalf("replace target %q is not absolute; a relative path resolves against the temp dir and breaks tidy", target)
	}
}

func TestModuleGoModKeepsAbsoluteReplacePath(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "repo")
	got, err := moduleGoMod(abs)
	if err != nil {
		t.Fatalf("moduleGoMod: %v", err)
	}
	if !strings.Contains(got, "=> "+abs+"\n") {
		t.Fatalf("absolute repo root not preserved in:\n%s", got)
	}
}

func TestGoBlocksReportRemovedSDKSymbols(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	content := t.TempDir()
	if err := os.WriteFile(filepath.Join(content, "guide.md"), []byte("```go\nsw.NoSuchPublicSDKFunction()\n```\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if checkGoBlocks(content, root) {
		t.Fatal("removed SDK symbol passed the documentation compiler")
	}
}
