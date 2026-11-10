package jobs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWindowsCompileArchitectures(t *testing.T) {
	for _, tc := range []struct {
		name, file, source string
		want               []string
	}{
		{"portable", "p.go", "package p\n", nil},
		{"portable name", "windows_helper.go", "package p\n", nil},
		{"negative tag", "p.go", "//go:build !windows\n\npackage p\n", nil},
		{"filename", "p_windows_test.go", "package p\n", []string{"amd64"}},
		{"tag", "p.go", "//go:build windows || linux\n\npackage p\n", []string{"amd64"}},
		{"arm64 filename", "p_windows_arm64.go", "package p\n", []string{"amd64", "arm64"}},
		{"arm64 tag", "p.go", "//go:build windows && arm64\n\npackage p\n", []string{"amd64", "arm64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGoFile(t, filepath.Join(dir, tc.file), tc.source)
			got, err := windowsCompileArchitectures(dir)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("architectures = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestWindowsCompilePropagatesCompilerFailureAndCleansOutputs(t *testing.T) {
	root := gateFixtureRepo(t)
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	file := filepath.Join(root, "internal", "broken_windows_test.go")
	writeGoFile(t, file, "package internal\n\nvar _ = missingWindowsIdentifier\n")
	ctx := newTestContext(t)
	writeGoFile(t, filepath.Join(root, "untouched", "bad_windows.go"), "package untouched\nvar _ = missingUntouchedIdentifier\n")
	err := runWindowsCompile(ctx)
	if err == nil || !strings.Contains(err.Error(), "windows/amd64") {
		t.Fatalf("bad Windows source accepted: %v", err)
	}
	writeGoFile(t, file, "package internal\n\nvar _ = Sound()\n")
	if err := runWindowsCompile(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("compile outputs retained: %v, %v", entries, err)
	}
}

func TestWindowsCompileChecksArm64OnlyWhenItsSourceExists(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "bad_windows_arm64_test.go"), "package internal\nvar _ = missingArm64Identifier\n")
	err := runWindowsCompile(newTestContext(t))
	if err == nil || !strings.Contains(err.Error(), "windows/arm64") {
		t.Fatalf("arm64-specific failure = %v", err)
	}
}
