package main

import (
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
