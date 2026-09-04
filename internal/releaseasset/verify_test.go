package releaseasset

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifyBindsAssetAndManifestToOneTrustedKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Binary: Sparkwing, GOOS: "windows", GOARCH: "arm64"}
	name, err := target.Name()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("runner")
	digest := sha256.Sum256(body)
	manifest := []byte(hex.EncodeToString(digest[:]) + "  " + name + "\n")
	verified, err := Verify([]ed25519.PublicKey{publicKey}, manifest, ed25519.Sign(privateKey, manifest), target, body, ed25519.Sign(privateKey, body))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Name() != name || verified.Digest() != hex.EncodeToString(digest[:]) {
		t.Fatalf("Verify() = %+v", verified)
	}

	otherPublic, otherPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify([]ed25519.PublicKey{publicKey, otherPublic}, manifest, ed25519.Sign(privateKey, manifest), target, body, ed25519.Sign(otherPrivate, body))
	if err == nil {
		t.Fatal("manifest and asset signatures from different trusted keys were accepted")
	}
}

func TestIdentityProbeRejectsAReplacedOrSymlinkedStage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link replacement fixture is Unix-only")
	}
	target := Target{Binary: SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	name, err := target.Name()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\nprintf '{\"binary\":\"sparkwing-runner\",\"version\":\"v1.2.3\",\"goos\":\"" + runtime.GOOS + "\",\"goarch\":\"" + runtime.GOARCH + "\"}\\n'\n")
	digest := sha256.Sum256(body)
	manifest := []byte(hex.EncodeToString(digest[:]) + "  " + name + "\n")
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := Verify([]ed25519.PublicKey{publicKey}, manifest,
		ed25519.Sign(privateKey, manifest), target, body, ed25519.Sign(privateKey, body))
	if err != nil {
		t.Fatal(err)
	}
	if identity, err := asset.VerifyExecutableIdentity(target, "v1.2.3"); err != nil {
		t.Fatalf("valid offline identity: %v", err)
	} else if identity.Binary != "sparkwing-runner" || identity.Version != "v1.2.3" {
		t.Fatalf("identity = %+v", identity)
	}
	if _, err := asset.VerifyExecutableIdentity(target, "v1.2.4"); err == nil {
		t.Fatal("version-mismatched identity passed")
	}

	t.Run("regular-file replacement", func(t *testing.T) {
		path, cleanup, err := asset.stageProbe(name)
		if err != nil {
			t.Fatal(err)
		}
		original := path + ".original"
		defer func() {
			_ = os.Remove(original)
			cleanup()
		}()
		_, err = asset.probeStagedIdentity(path, target, "v1.2.3", func(path string) {
			if err := os.Rename(path, original); err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(path, body, 0o700); err != nil {
				t.Error(err)
			}
		})
		if err == nil || !strings.Contains(err.Error(), "replaced") {
			t.Fatalf("replacement error = %v", err)
		}
	})

	t.Run("symbolic-link replacement", func(t *testing.T) {
		path, cleanup, err := asset.stageProbe(name)
		if err != nil {
			t.Fatal(err)
		}
		original := filepath.Join(filepath.Dir(path), "original")
		defer func() {
			_ = os.Remove(original)
			cleanup()
		}()
		_, err = asset.probeStagedIdentity(path, target, "v1.2.3", func(path string) {
			if err := os.Rename(path, original); err != nil {
				t.Error(err)
				return
			}
			if err := os.Symlink(original, path); err != nil {
				t.Error(err)
			}
		})
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("symlink error = %v", err)
		}
	})

	t.Run("cleanup does not follow a replaced directory", func(t *testing.T) {
		path, cleanup, err := asset.stageProbe(name)
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Dir(path)
		original := directory + ".original"
		outside := t.TempDir()
		marker := filepath.Join(outside, filepath.Base(path))
		if err := os.WriteFile(marker, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(directory, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, directory); err != nil {
			t.Fatal(err)
		}
		cleanup()
		body, err := os.ReadFile(marker)
		if err != nil || string(body) != "outside" {
			t.Fatalf("cleanup followed replacement: body=%q err=%v", body, err)
		}
		if err := os.Remove(filepath.Join(original, filepath.Base(path))); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(original); err != nil {
			t.Fatal(err)
		}
	})
}
