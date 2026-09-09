package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/mod/sumdb/dirhash"
)

func TestSelfModuleSumsNeedsNoTemporaryFile(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	writeSelfModuleSumsFixture(t, repository)
	const version = "v0.1.0"
	data, err := createSelfModuleZip(context.Background(), repository, version)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "module.zip")
	if err := os.WriteFile(archive, data, 0600); err != nil {
		t.Fatal(err)
	}
	want, err := dirhash.HashZip(archive, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(root, "absent"))
	got, _, err := selfModuleSums(context.Background(), repository, version)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("module hash = %s, want official ZIP hash %s", got, want)
	}
}

func TestReleaseScanDirectoryPreservesWorkAndCleanupErrors(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "temporary")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", parent)
	workErr := errors.New("scan failed")
	err := withReleaseScanDirectory(func(directory string) error {
		if err := os.Remove(directory); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(parent); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(parent, nil, 0600); err != nil {
			t.Fatal(err)
		}
		return workErr
	})
	if !errors.Is(err, workErr) || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("scan error = %v, want work failure and ENOTDIR", err)
	}
}
