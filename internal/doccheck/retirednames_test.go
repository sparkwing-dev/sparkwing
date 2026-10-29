package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryDocsMentionNoRetiredNames(t *testing.T) {
	if !checkRetiredNames(filepath.Join("..", "..")) {
		t.Fatal("docs, help text or current design docs name a retired surface")
	}
}

func TestRetiredNamesFailOnAPlantedMention(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{"set `--free-team-slots` on the controller", false},
		{"export SPARKWING_LOCAL_ONLY=1", false},
		{"`--free-team-slots-extra` is a different flag", true},
		{"SPARKWING_LOCAL_ONLY_X is not the retired variable", true},
	} {
		if err := os.WriteFile(filepath.Join(root, "docs", "guide.md"), []byte(tc.text), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := checkRetiredNames(root); got != tc.ok {
			t.Errorf("%q: passed=%v, want %v", tc.text, got, tc.ok)
		}
	}
}
