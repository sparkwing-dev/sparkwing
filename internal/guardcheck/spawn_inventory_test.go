package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestSpawnInventoryFailsClosedAcrossBuildProfiles(t *testing.T) {
	root := t.TempDir()
	writeSpawnInventoryFixture(t, root, map[string]string{
		"go.mod":                      "module " + modulePath + "\n\ngo 1.26.0\n",
		"ordinary/source.go":          "package ordinary\n",
		"testonly/source_test.go":     "package testonly\n",
		"tagged/source.go":            "//go:build qatrust\n\npackage tagged\nimport \"os/exec\"\nvar factory = exec.Command\n",
		"native/source.go":            "//go:build cgo\n\npackage native\nimport \"C\"\nimport \"os/exec\"\nvar factory = exec.Command\nvar value = C.int(1)\n",
		"bsd/source_freebsd.go":       "package bsd\nimport \"os/exec\"\nvar factory = exec.Command\n",
		"unregistered/source.go":      "//go:build newprofile\n\npackage unregistered\nimport \"os/exec\"\nvar factory = exec.Command\n",
		"unsupported/source_plan9.go": "package unsupported\nimport \"os/exec\"\nvar factory = exec.Command\n",
	})
	var spawns []string
	uncovered, err := loadSpawnPackages(root, func(pkg *packages.Package) {
		spawns = append(spawns, packageSpawnFindings(pkg)...)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"tagged", "native", "bsd"} {
		if !strings.Contains(strings.Join(spawns, "\n"), filepath.Join(root, dir)) {
			t.Errorf("raw spawn in %s escaped analysis: %v", dir, spawns)
		}
	}
	if len(uncovered) != 2 {
		t.Fatalf("want two uncovered-source failures, got %v", uncovered)
	}
	for _, name := range []string{"unregistered/source.go", "unsupported/source_plan9.go"} {
		if !strings.Contains(strings.Join(uncovered, "\n"), filepath.FromSlash(name)) {
			t.Errorf("missing uncovered source %s: %v", name, uncovered)
		}
	}
}

func TestSpawnInventoryUsesGoModuleTraversal(t *testing.T) {
	root := t.TempDir()
	writeSpawnInventoryFixture(t, root, map[string]string{
		"go.mod":             "module example.test/inventory\n\ngo 1.26.0\n",
		"source.go":          "package inventory\n",
		"source_test.go":     "package inventory\n",
		"nested/go.mod":      "module example.test/nested\n",
		"nested/source.go":   "package nested\n",
		"testdata/source.go": "package ignored\n",
		"vendor/source.go":   "package ignored\n",
		".hidden/source.go":  "package ignored\n",
		"_ignored/source.go": "package ignored\n",
		".source.go":         "package ignored\n",
		"_source.go":         "package ignored\n",
	})
	files, err := spawnSourceInventory(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, []string{filepath.Join(root, "source.go")}) {
		t.Fatalf("unexpected module source inventory: %v", files)
	}
}

func writeSpawnInventoryFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, source := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
