package main

import (
	"path/filepath"
	"testing"
)

func TestCheckLinks(t *testing.T) {
	for _, tc := range []struct {
		name, source, target string
	}{
		{"changelog fragment", "docs/changes/change.md", "docs/migrations/unreleased/change.md"},
		{"documentation page", "docs/guide.md", "migrations/unreleased/change.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			content := filepath.Join(root, "docs")
			writeRepoFile(t, root, tc.source, "[migration]("+tc.target+"#change)\n")
			if checkLinks(content) {
				t.Fatal("missing migration target must fail")
			}
			writeRepoFile(t, root, "docs/migrations/unreleased/change.md", "## Change\n\nUpdate the configuration.\n")
			if !checkLinks(content) {
				t.Fatal("existing migration target must pass")
			}
		})
	}
}
