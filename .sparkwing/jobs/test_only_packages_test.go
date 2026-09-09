package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSkipsTestOnlyPackageAndTestRunsIt(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "checks", "checks_test.go"), "package checks\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) { t.Fatal(\"fixture failure\") }\n")
	gitAddAll(t, root)
	if err := runBuild(context.Background()); err != nil {
		t.Fatalf("build rejected a test-only package: %v", err)
	}
	if err := runTest(context.Background()); err == nil || !strings.Contains(err.Error(), "fixture failure") {
		t.Fatalf("test verdict = %v, want fixture test failure", err)
	}
}

func TestVetChecksTestOnlyPackage(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "checks", "checks_test.go"), "package checks\n\nfunc broken(\n")
	gitAddAll(t, root)
	if err := runVet(context.Background()); err == nil {
		t.Fatal("vet skipped a malformed test-only package")
	}
}

func TestBuildRejectsMalformedPackageDeclaration(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "broken", "source.go"), "package\n")
	gitAddAll(t, root)
	if err := runBuild(context.Background()); err == nil {
		t.Fatal("build skipped a malformed package declaration")
	}
}
