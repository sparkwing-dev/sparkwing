package jobs

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTheTestStepKeepsLiveServicesAwayFromTheSuitesItRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.7s of real work; the fast class runs under -short")
	}
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "internal", "ambient_probe_test.go"), ambientServiceProbe)
	gitAddAll(t, root)

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "dev.env"),
		[]byte("SPARKWING_CONTROLLER_URL=http://127.0.0.1:4344\n"), 0o600); err != nil {
		t.Fatalf("write dev.env: %v", err)
	}
	t.Setenv(productTestHomeVar, home)
	t.Setenv("SPARKWING_CONTROLLER_URL", "http://127.0.0.1:4344")
	t.Setenv("SPARKWING_CACHE_URL", "http://127.0.0.1:8090")
	t.Setenv("SPARKWING_AGENT_TOKEN", "an inherited bearer")

	if err := forEachGoModule(ctx, "go test", "go test ./...", ""); err == nil {
		t.Fatal("the probe must fail while the ambient bindings reach it, or the pass below proves nothing")
	}
	if err := runTest(ctx); err != nil {
		t.Fatalf("the test step handed its suites a live service or the dev.env fallback: %v", err)
	}
}

// safety: composeRuntime is the one place a node child's run bindings are
// written, each through put, so its put calls are the names this covers.
var injectedName = regexp.MustCompile(`\bput\("([A-Z][A-Z0-9_]*)"`)

func readProductSource(t *testing.T, parts ...string) string {
	t.Helper()
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path; " +
			"if this package moved, check sourceTreeRoot's markers")
	}
	data, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return string(data)
}

func TestEveryVariableTheNodeInjectorSetsIsScrubbedPinnedOrRecorded(t *testing.T) {
	source := readProductSource(t, "internal", "procenv", "build.go")
	_, injector, found := strings.Cut(source, "func composeRuntime(")
	if !found {
		t.Fatal("internal/procenv/build.go no longer defines composeRuntime; point this contract at the new injector")
	}
	injector, _, _ = strings.Cut(injector, "\n}\n")
	injected := map[string]bool{}
	for _, m := range injectedName.FindAllStringSubmatch(injector, -1) {
		injected[m[1]] = true
	}
	if len(injected) == 0 {
		t.Fatal("read no variable names out of the node injector, so this contract checks nothing")
	}

	handled := map[string]bool{}
	for _, name := range productTestUnset {
		handled[name] = true
	}
	for _, pin := range productTestPins(t.TempDir()) {
		name, _, _ := strings.Cut(pin, "=")
		handled[name] = true
	}
	for name := range productTestKept {
		handled[name] = true
	}

	for name := range injected {
		if !handled[name] {
			t.Errorf("the node injector hands a child %s, which no test step clears or pins; "+
				"add it to productTestUnset, or to productTestKept with the reason it is harmless", name)
		}
	}
	for name, why := range productTestKept {
		if !injected[name] {
			t.Errorf("productTestKept keeps %s (%s), which the node injector no longer sets; drop it", name, why)
		}
	}
}

const ambientServiceProbe = `package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoLiveServiceIsWithinReach(t *testing.T) {
	for _, name := range []string{
		"SPARKWING_CONTROLLER_URL",
		"SPARKWING_CACHE_URL",
		"SPARKWING_AGENT_TOKEN",
		"SPARKWING_RUN_ID",
		"SPARKWING_NODE_ID",
	} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s=%s reached the suite", name, value)
		}
	}
	home := os.Getenv("SPARKWING_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".sparkwing")
	}
	if _, err := os.Stat(filepath.Join(home, "dev.env")); err == nil {
		t.Fatalf("the home %s carries a dev.env this suite can read", home)
	}
}
`
