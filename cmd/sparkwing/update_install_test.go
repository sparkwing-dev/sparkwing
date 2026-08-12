package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallVerifiedAssetRejectsMutationAfterVerification(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	old := []byte("old binary")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	asset := testVerifiedAsset([]byte("signed binary"))

	originalMutate := updateMutateStaged
	originalReplace := updateReplace
	originalRestore := updateRestore
	t.Cleanup(func() {
		updateMutateStaged = originalMutate
		updateReplace = originalReplace
		updateRestore = originalRestore
	})
	updateMutateStaged = func(path string) {
		if err := os.WriteFile(path, []byte("mutated after verification"), 0o755); err != nil {
			t.Error(err)
		}
	}
	updateReplace = os.Rename
	updateRestore = os.Rename

	if err := installVerifiedAsset(asset, target); err == nil {
		t.Fatal("installVerifiedAsset() succeeded after staged bytes changed")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("installed bytes = %q, want original %q", got, old)
	}
}

func TestInstallVerifiedAssetRestoresAfterInstalledDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	old := []byte("old binary")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	asset := testVerifiedAsset([]byte("signed binary"))

	originalMutate := updateMutateStaged
	originalReplace := updateReplace
	originalRestore := updateRestore
	t.Cleanup(func() {
		updateMutateStaged = originalMutate
		updateReplace = originalReplace
		updateRestore = originalRestore
	})
	updateMutateStaged = func(string) {}
	replacements := 0
	updateReplace = func(source, destination string) error {
		replacements++
		if err := os.Rename(source, destination); err != nil {
			return err
		}
		if replacements == 1 {
			return os.WriteFile(destination, []byte("corrupt installed bytes"), 0o755)
		}
		return nil
	}
	updateRestore = updateReplace

	if err := installVerifiedAsset(asset, target); err == nil {
		t.Fatal("installVerifiedAsset() succeeded after installed bytes changed")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("installed bytes = %q, want restored %q", got, old)
	}
	if replacements != 2 {
		t.Fatalf("replacement calls = %d, want install plus rollback", replacements)
	}
}

func TestInstallVerifiedAssetRestoresWhenDirectorySyncFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	old := []byte("old binary")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	asset := testVerifiedAsset([]byte("signed binary"))

	originalMutate := updateMutateStaged
	originalReplace := updateReplace
	originalRestore := updateRestore
	originalSync := updateSyncDir
	t.Cleanup(func() {
		updateMutateStaged = originalMutate
		updateReplace = originalReplace
		updateRestore = originalRestore
		updateSyncDir = originalSync
	})
	updateMutateStaged = func(string) {}
	updateReplace = os.Rename
	updateRestore = os.Rename
	syncCalls := 0
	updateSyncDir = func(string) error {
		syncCalls++
		if syncCalls == 1 {
			return errors.New("directory sync failed")
		}
		return nil
	}

	if err := installVerifiedAsset(asset, target); err == nil {
		t.Fatal("installVerifiedAsset() succeeded after directory sync failed")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("installed bytes = %q, want restored %q", got, old)
	}
	if syncCalls != 2 {
		t.Fatalf("directory sync calls = %d, want failed install plus restored rollback", syncCalls)
	}
}

func testVerifiedAsset(body []byte) verifiedReleaseAsset {
	digest := sha256.Sum256(body)
	return verifiedReleaseAsset{name: "sparkwing-test", bytes: body, digest: hex.EncodeToString(digest[:])}
}
