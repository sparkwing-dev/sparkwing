package main

import (
	"slices"
	"testing"
)

func TestDiscoverPackagePathsIncludesEveryPublicPackage(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := discoverPackagePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sparkwing", "pkg/cachecontrol", "pkg/storage/fs"} {
		if !slices.Contains(paths, want) {
			t.Errorf("public package %q is absent from API snapshots", want)
		}
	}
	if !slices.IsSorted(paths) {
		t.Fatalf("package paths are not stable: %v", paths)
	}
}
