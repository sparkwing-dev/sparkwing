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
	for _, recipe := range []string{"sparkwing-cache", "gitcache", "RetryAuto()", "cache-grant-key", "SPARKWING_GITCACHE_URL", "SPARKWING_CACHE_URL", "SPARKWING_CACHE_GRANT", "A child always uses its parent's repository", "A child always uses the parent's repository"} {
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

func TestBannedTokensRecognizesOnlyAuthoritativeEnvironmentRejections(t *testing.T) {
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
	row := "| `SPARKWING_CACHE_GRANT` | Credential | Removed transport; forbidden in all Build output. |"
	for _, tc := range []struct {
		name, page, text string
		allowed          bool
	}{
		{"registry rejection", "environment-variables.md", row, true},
		{"recipe beside rejection", "environment-variables.md", row + "\nexport SPARKWING_CACHE_GRANT=value", false},
		{"rejection in guide", "guide.md", row, false},
		{"false classification", "environment-variables.md", "| `SPARKWING_CACHE_GRANT` | Tool | Removed transport; forbidden in all Build output. |", false},
		{"false policy", "environment-variables.md", "| `SPARKWING_CACHE_GRANT` | Credential | Use for caching. |", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(docs, tc.page)
			if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := checkBannedTokens(docs, root); got != tc.allowed {
				t.Fatalf("rejection policy allowed=%v, want %v", got, tc.allowed)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}
