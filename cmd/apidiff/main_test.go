package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRepoRootFindsModuleWithLFOrCRLF(t *testing.T) {
	for _, lineEnding := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[lineEnding], func(t *testing.T) {
			root := t.TempDir()
			mod := "module github.com/sparkwing-dev/sparkwing" + lineEnding + lineEnding + "go 1.26.0" + lineEnding
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0o644); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(root, "nested")
			if err := os.Mkdir(child, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(child, "go.mod"), []byte("module sparkwing-pipelines"+lineEnding), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := repoRootFrom(child)
			if err != nil || got != filepath.Clean(root) {
				t.Fatalf("repoRootFrom(%q) = %q, %v; want %q", child, got, err, root)
			}
		})
	}
}

func TestDiscoverPackagePathsIncludesEveryPublicPackage(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := discoverPackagePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sparkwing", "pkg/cachecontrol", "pkg/wingwire"} {
		if !slices.Contains(paths, want) {
			t.Errorf("public package %q is absent from API snapshots", want)
		}
	}
	if !slices.IsSorted(paths) {
		t.Fatalf("package paths are not stable: %v", paths)
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if seen[path] {
			t.Fatalf("public package %q appears more than once: %v", path, paths)
		}
		seen[path] = true
	}
}

func TestDiscoverPackagePathsExcludesExecutionFixtures(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"pkg/public", "pkg/public/testdata/native-sdk"} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "main.go"), []byte("package fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := discoverPackagePaths(root)
	if err != nil || !slices.Equal(paths, []string{"pkg/public", "sparkwing"}) {
		t.Fatalf("execution fixture became a public API: %v %v", paths, err)
	}
}
