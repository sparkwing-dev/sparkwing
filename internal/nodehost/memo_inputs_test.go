package nodehost

import (
	"os"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestEnvDeterministicOrder(t *testing.T) {
	t.Setenv("SAMPLE_FIRST_VALUE", "1")
	t.Setenv("SAMPLE_SECOND_VALUE", "2")

	first := resolvedKey(t, inputs.Env("SAMPLE_FIRST_VALUE", "SAMPLE_SECOND_VALUE"))
	second := resolvedKey(t, inputs.Env("SAMPLE_SECOND_VALUE", "SAMPLE_FIRST_VALUE"))
	if first != second || first == "" {
		t.Fatalf("Env order should not affect hash: a=%q b=%q", first, second)
	}
}

func TestEnvUnsetVsEmpty(t *testing.T) {
	t.Setenv("SAMPLE_ENV_VALUE", "")
	recipe := inputs.Env("SAMPLE_ENV_VALUE")
	present := resolvedKey(t, recipe)
	if err := os.Unsetenv("SAMPLE_ENV_VALUE"); err != nil {
		t.Fatal(err)
	}
	if absent := resolvedKey(t, recipe); present == absent {
		t.Fatal("unset and empty share a memo key")
	}
}

func TestEnvValueChangesHash(t *testing.T) {
	t.Setenv("SAMPLE_INPUT_VALUE", "one")
	first := resolvedKey(t, inputs.Env("SAMPLE_INPUT_VALUE"))
	t.Setenv("SAMPLE_INPUT_VALUE", "two")
	second := resolvedKey(t, inputs.Env("SAMPLE_INPUT_VALUE"))
	if first == second {
		t.Fatalf("changing var value should change hash: a=%q b=%q", first, second)
	}
}

func TestEnvFramesNamesAndValues(t *testing.T) {
	t.Setenv("MEMO_FRAME_A", "x\x1eMEMO_FRAME_B=y")
	t.Setenv("MEMO_FRAME_B", "z")
	first := resolvedKey(t, inputs.Env("MEMO_FRAME_A", "MEMO_FRAME_B"))
	t.Setenv("MEMO_FRAME_A", "x")
	t.Setenv("MEMO_FRAME_B", "y\x1eMEMO_FRAME_B=z")
	if second := resolvedKey(t, inputs.Env("MEMO_FRAME_A", "MEMO_FRAME_B")); first == second {
		t.Fatalf("different environment values share a key: %q", first)
	}
}

func TestConst(t *testing.T) {
	if resolvedKey(t, inputs.Const("v1")) == "" {
		t.Fatal("Const should yield a host key")
	}
	if resolvedKey(t, inputs.Const("v1")) == resolvedKey(t, inputs.Const("v2")) {
		t.Fatal("different Const values should differ")
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
	if matchPath(t, matcher, "nested/docs/x") {
		t.Error("docs/ must start at the checkout root")
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

func TestComposeIncorporatesAllParts(t *testing.T) {
	first := resolvedKey(t, inputs.Compose(inputs.Const("x"), inputs.Const("y")))
	second := resolvedKey(t, inputs.Compose(inputs.Const("x"), inputs.Const("z")))
	if first == second {
		t.Fatal("changing one Const should change Compose output")
	}
	if first == "" {
		t.Errorf("Compose output must be a memo key: %q", first)
	}
}

func resolvedKey(t *testing.T, resolve sparkwing.MemoInput) string {
	t.Helper()
	key, err := resolveTestMemo(t, resolve)
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

func resolveTestMemo(t *testing.T, input sparkwing.MemoInput) (string, error) {
	t.Helper()
	workspace, err := os.Getwd()
	if err != nil {
		return "", err
	}
	environment, err := procenv.Build(procenv.Attempt, procenv.Base{Local: true, Captured: os.Environ()}, procenv.Runtime{})
	if err != nil {
		return "", err
	}
	value, err := memoInputValue(t.Context(), input, workspace, environment)
	if err != nil {
		return "", err
	}
	return memoValueKey(value)
}

func TestEnvUsesPlatformNameComparison(t *testing.T) {
	first, err := procenv.Build(procenv.Attempt, procenv.Base{Local: true, Captured: []string{"Memo_Path=one"}}, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := procenv.Build(procenv.Attempt, procenv.Base{Local: true, Captured: []string{"Memo_Path=two"}}, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	recipe := inputs.Env("MEMO_PATH")
	a, err := memoInputValue(t.Context(), recipe, "", first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := memoInputValue(t.Context(), recipe, "", second)
	if err != nil {
		t.Fatal(err)
	}
	keyA, _ := memoValueKey(a)
	keyB, _ := memoValueKey(b)
	if changed := keyA != keyB; changed != (runtime.GOOS == "windows") {
		t.Fatalf("environment change affected memo = %v on %s", changed, runtime.GOOS)
	}
}
