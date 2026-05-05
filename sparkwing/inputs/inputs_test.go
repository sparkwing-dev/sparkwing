package inputs

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestEnvDeterministicOrder(t *testing.T) {
	t.Setenv("FOO", "1")
	t.Setenv("BAR", "2")

	a := Env("FOO", "BAR")(context.Background())
	b := Env("BAR", "FOO")(context.Background())
	if a != b || a == "" {
		t.Fatalf("Env order should not affect hash: a=%q b=%q", a, b)
	}
}

func TestEnvUnsetVsEmpty(t *testing.T) {
	t.Setenv("PRESENT_BUT_EMPTY", "")
	os.Unsetenv("ABSENT_FOR_TEST_XYZ")

	present := Env("PRESENT_BUT_EMPTY")(context.Background())
	absent := Env("ABSENT_FOR_TEST_XYZ")(context.Background())
	if present == absent {
		t.Fatalf("set-but-empty and unset must hash differently: both=%q", present)
	}
}

func TestEnvValueChangesHash(t *testing.T) {
	t.Setenv("VAR", "one")
	a := Env("VAR")(context.Background())
	t.Setenv("VAR", "two")
	b := Env("VAR")(context.Background())
	if a == b {
		t.Fatalf("changing var value should change hash: a=%q b=%q", a, b)
	}
}

func TestConst(t *testing.T) {
	if Const("v1")(context.Background()) != "v1" {
		t.Fatal("Const should return its arg verbatim")
	}
	if Const("v1")(context.Background()) == Const("v2")(context.Background()) {
		t.Fatal("different Const values should differ")
	}
}

func TestComposeShortCircuitsOnEmpty(t *testing.T) {
	empty := sparkwing.CacheKeyFn(func(context.Context) sparkwing.CacheKey { return "" })
	v := sparkwing.CacheKeyFn(func(context.Context) sparkwing.CacheKey { return "x" })
	got := Compose(v, empty)(context.Background())
	if got != "" {
		t.Fatalf("Compose with any empty sub-fn must return empty, got %q", got)
	}
}

func TestCompilePattern_Basename(t *testing.T) {
	m := compilePattern("*.md")
	for _, p := range []string{"README.md", "docs/api.md", "deep/nested/x.md"} {
		if !m(p) {
			t.Errorf("expected basename match for %q", p)
		}
	}
	for _, p := range []string{"src/foo.tsx", "Makefile", "x.mdx"} {
		if m(p) {
			t.Errorf("did not expect match for %q", p)
		}
	}
}

func TestCompilePattern_DirPrefix(t *testing.T) {
	m := compilePattern("docs/")
	for _, p := range []string{"docs/api.md", "docs/nested/foo.txt"} {
		if !m(p) {
			t.Errorf("expected dir-prefix match for %q", p)
		}
	}
	if m("documents/x") {
		t.Error("docs/ should not match documents/")
	}
	if m("README.md") {
		t.Error("docs/ should not match top-level files")
	}
}

func TestCompilePattern_DoubleStar(t *testing.T) {
	m := compilePattern("docs/**/*.md")
	if !m("docs/api.md") {
		t.Error("docs/**/*.md should match docs/api.md")
	}
	if !m("docs/sub/page.md") {
		t.Error("docs/**/*.md should match docs/sub/page.md")
	}
	if m("src/api.md") {
		t.Error("docs/**/*.md should not match src/api.md")
	}
}

func TestCompilePattern_ExactPath(t *testing.T) {
	m := compilePattern("CI_TRADEOFFS.md")
	// No slash → basename match anywhere.
	if !m("CI_TRADEOFFS.md") {
		t.Error("expected exact match")
	}
	if !m("subdir/CI_TRADEOFFS.md") {
		t.Error("basename match should reach into subdirs")
	}
}

func TestIgnoreMatcherDropsMatched(t *testing.T) {
	keep := buildIgnoreMatcher([]string{"*.md", "docs/"})
	cases := map[string]bool{ // path -> expected keep
		"src/foo.tsx":       true,
		"README.md":         false,
		"docs/api.md":       false,
		"package.json":      true,
		"docs/deep/img.png": false,
	}
	for path, want := range cases {
		if got := keep(path); got != want {
			t.Errorf("keep(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIncludeMatcherKeepsMatched(t *testing.T) {
	keep := buildIncludeMatcher([]string{"src/**", "package.json"})
	cases := map[string]bool{
		"src/foo.tsx":  true,
		"src/sub/x.ts": true,
		"package.json": true,
		"README.md":    false,
		"docs/api.md":  false,
	}
	for path, want := range cases {
		if got := keep(path); got != want {
			t.Errorf("keep(%q) = %v, want %v", path, got, want)
		}
	}
}

// Sanity that globToRegex anchors fully (rejects substring matches).
func TestGlobToRegexAnchored(t *testing.T) {
	re := globToRegex("docs/api.md")
	if re.MatchString("a-docs/api.md-suffix") {
		t.Error("glob should be anchored, not substring")
	}
	if !re.MatchString("docs/api.md") {
		t.Error("glob should match exact path")
	}
}

// Ensure ignore patterns ending in `/` only match the prefix, not a
// shorter prefix that happens to share characters. Regression guard.
func TestDirPrefixIsBoundaryAware(t *testing.T) {
	m := compilePattern("doc/")
	if m("docs/x") {
		t.Error("doc/ must not match docs/x")
	}
}

// Composability: helpers should be assignable directly to a
// CacheKeyFn-typed slot without ceremony.
func TestSignatureIsCacheKeyFn(t *testing.T) {
	// Compile-time check: assign each constructor result to an
	// untyped variable; type inference picks sparkwing.CacheKeyFn.
	_ = RepoFiles()
	_ = RepoFiles(Ignore("*.md"))
	_ = Files("src/**")
	_ = Env("HOME")
	_ = Const("v1")
	_ = Compose(Const("a"), Const("b"))
}

// Smoke: composed key respects all parts.
func TestComposeIncorporatesAllParts(t *testing.T) {
	a := Compose(Const("x"), Const("y"))(context.Background())
	b := Compose(Const("x"), Const("z"))(context.Background())
	if a == b {
		t.Fatal("changing one Const should change Compose output")
	}
	if !strings.HasPrefix(string(a), "ck:") {
		t.Errorf("Compose output should be sparkwing.Key prefix: %q", a)
	}
}
