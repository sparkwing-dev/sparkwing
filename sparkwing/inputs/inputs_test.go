package inputs

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestEnvDeterministicOrder(t *testing.T) {
	t.Setenv("SAMPLE_FIRST_VALUE", "1")
	t.Setenv("SAMPLE_SECOND_VALUE", "2")

	first := resolvedKey(t, Env("SAMPLE_FIRST_VALUE", "SAMPLE_SECOND_VALUE"))
	second := resolvedKey(t, Env("SAMPLE_SECOND_VALUE", "SAMPLE_FIRST_VALUE"))
	if first != second || first == "" {
		t.Fatalf("Env order should not affect hash: a=%q b=%q", first, second)
	}
}

func TestEnvUnsetVsEmpty(t *testing.T) {
	t.Setenv("SAMPLE_PRESENT_VALUE", "")
	t.Setenv("SAMPLE_ABSENT_VALUE", "")
	if err := os.Unsetenv("SAMPLE_ABSENT_VALUE"); err != nil {
		t.Fatal(err)
	}

	present := resolvedKey(t, Env("SAMPLE_PRESENT_VALUE"))
	absent := resolvedKey(t, Env("SAMPLE_ABSENT_VALUE"))
	if present == absent {
		t.Fatalf("set-but-empty and unset must hash differently: both=%q", present)
	}
}

func TestEnvValueChangesHash(t *testing.T) {
	t.Setenv("SAMPLE_INPUT_VALUE", "one")
	first := resolvedKey(t, Env("SAMPLE_INPUT_VALUE"))
	t.Setenv("SAMPLE_INPUT_VALUE", "two")
	second := resolvedKey(t, Env("SAMPLE_INPUT_VALUE"))
	if first == second {
		t.Fatalf("changing var value should change hash: a=%q b=%q", first, second)
	}
}

func TestConst(t *testing.T) {
	if resolvedKey(t, Const("v1")) != "v1" {
		t.Fatal("Const should return its supplied key")
	}
	if resolvedKey(t, Const("v1")) == resolvedKey(t, Const("v2")) {
		t.Fatal("different Const values should differ")
	}
}

func TestComposeShortCircuitsOnEmpty(t *testing.T) {
	empty := sparkwing.CacheKeyFn(func(context.Context) (sparkwing.CacheKey, error) { return "", nil })
	value := sparkwing.CacheKeyFn(func(context.Context) (sparkwing.CacheKey, error) { return "x", nil })
	key, err := Compose(value, empty)(t.Context())
	if key != "" || err == nil || !strings.Contains(err.Error(), "empty key") {
		t.Fatalf("empty input = (%q, %v)", key, err)
	}
}

func TestCompilePattern_Basename(t *testing.T) {
	matcher, err := compilePattern("*.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"README.md", "docs/api.md", "deep/nested/x.md"} {
		if !matchPath(t, matcher, path) {
			t.Errorf("expected basename match for %q", path)
		}
	}
	for _, path := range []string{"src/foo.tsx", "Makefile", "x.mdx"} {
		if matchPath(t, matcher, path) {
			t.Errorf("did not expect match for %q", path)
		}
	}
}

func TestCompilePattern_DirPrefix(t *testing.T) {
	matcher, err := compilePattern("docs/")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"docs/api.md", "docs/nested/foo.txt"} {
		if !matchPath(t, matcher, path) {
			t.Errorf("expected directory prefix match for %q", path)
		}
	}
	if matchPath(t, matcher, "documents/x") {
		t.Error("docs/ should not match documents/")
	}
	if matchPath(t, matcher, "README.md") {
		t.Error("docs/ should not match top-level files")
	}
}

func TestCompilePattern_DoubleStar(t *testing.T) {
	matcher, err := compilePattern("docs/**/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if !matchPath(t, matcher, "docs/api.md") {
		t.Error("docs/**/*.md should match docs/api.md")
	}
	if !matchPath(t, matcher, "docs/sub/page.md") {
		t.Error("docs/**/*.md should match docs/sub/page.md")
	}
	if matchPath(t, matcher, "src/api.md") {
		t.Error("docs/**/*.md should not match src/api.md")
	}
}

func TestCompilePattern_ExactPath(t *testing.T) {
	matcher, err := compilePattern("sample-notes.md")
	if err != nil {
		t.Fatal(err)
	}
	if !matchPath(t, matcher, "sample-notes.md") {
		t.Error("expected exact match")
	}
	if !matchPath(t, matcher, "subdir/sample-notes.md") {
		t.Error("basename match should reach into subdirectories")
	}
}

func TestIgnoreMatcherDropsMatched(t *testing.T) {
	keep, err := buildIgnoreMatcher([]string{"*.md", "docs/"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"src/foo.tsx":       true,
		"README.md":         false,
		"docs/api.md":       false,
		"package.json":      true,
		"docs/deep/img.png": false,
	}
	for path, want := range cases {
		if got := matchPath(t, keep, path); got != want {
			t.Errorf("keep(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIncludeMatcherKeepsMatched(t *testing.T) {
	keep, err := buildIncludeMatcher([]string{"src/**", "package.json"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"src/foo.tsx":  true,
		"src/sub/x.ts": true,
		"package.json": true,
		"README.md":    false,
		"docs/api.md":  false,
	}
	for path, want := range cases {
		if got := matchPath(t, keep, path); got != want {
			t.Errorf("keep(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestGlobToRegexAnchored(t *testing.T) {
	expression := globToRegex("docs/api.md")
	if expression.MatchString("a-docs/api.md-suffix") {
		t.Error("glob should be anchored, not substring")
	}
	if !expression.MatchString("docs/api.md") {
		t.Error("glob should match exact path")
	}
}

func TestDirPrefixIsBoundaryAware(t *testing.T) {
	matcher, err := compilePattern("doc/")
	if err != nil {
		t.Fatal(err)
	}
	if matchPath(t, matcher, "docs/x") {
		t.Error("doc/ must not match docs/x")
	}
}

func TestSignatureIsCacheKeyFn(t *testing.T) {
	_ = RepoFiles()
	_ = RepoFiles(Ignore("*.md"))
	_ = Files("src/**")
	_ = Env("HOME")
	_ = Const("v1")
	_ = Compose(Const("a"), Const("b"))
}

func TestComposeIncorporatesAllParts(t *testing.T) {
	first := resolvedKey(t, Compose(Const("x"), Const("y")))
	second := resolvedKey(t, Compose(Const("x"), Const("z")))
	if first == second {
		t.Fatal("changing one Const should change Compose output")
	}
	if !strings.HasPrefix(string(first), "ck:") {
		t.Errorf("Compose output should be sparkwing.Key prefix: %q", first)
	}
}

func resolvedKey(t *testing.T, resolve sparkwing.CacheKeyFn) sparkwing.CacheKey {
	t.Helper()
	key, err := resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func matchPath(t *testing.T, matcher pathMatcher, path string) bool {
	t.Helper()
	matched, err := matcher(path)
	if err != nil {
		t.Fatal(err)
	}
	return matched
}
