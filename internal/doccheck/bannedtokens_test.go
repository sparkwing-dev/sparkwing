package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDocsRefuseRemovedCacheAndRetryRecipes(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	for _, dir := range []string{docs, filepath.Join(root, "cmd", "sparkwing")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "sparkwing", "help_registry.go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, recipe := range []string{"sparkwing-cache", "gitcache", "RetryAuto()", "cache-grant-key", "SPARKWING_GITCACHE_URL", "SPARKWING_CACHE_URL", "SPARKWING_CACHE_GRANT"} {
		t.Run(recipe, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(docs, "guide.md"), []byte(recipe), 0o600); err != nil {
				t.Fatal(err)
			}
			if checkBannedTokens(docs, root) {
				t.Fatal("removed surface passed the documentation check")
			}
		})
	}
}

func TestBanned_FlagsPhantomPipelineAddVerb(t *testing.T) {
	const phantom = "register the repo with `sparkwing pipeline add <path>`"
	for _, b := range banned {
		if b.re.MatchString(phantom) {
			return
		}
	}
	t.Fatalf("no banned pattern flags the phantom `sparkwing pipeline add` verb")
}
