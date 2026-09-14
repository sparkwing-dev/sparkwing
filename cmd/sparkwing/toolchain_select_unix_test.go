//go:build !windows

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const toolchainExecHelperEnv = "SPARKWING_TOOLCHAIN_EXEC_HELPER"

func releaseFixture(version string) []byte {
	return []byte("#!/bin/sh\n" +
		`if [ "$1" = "version" ]; then printf '{"cli":{"installed":"` + version + `"}}\n'; exit 0; fi` + "\n" +
		"echo \"fixture argv: $@\"\n" +
		"echo \"fixture active: $SPARKWING_TOOLCHAIN_ACTIVE\"\n" +
		"exit 7\n")
}

func seedToolchainStore(t *testing.T, version string, asset []byte) (home, binPath string, priv ed25519.PrivateKey) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "fresh-home")
	t.Setenv("SPARKWING_HOME", home)
	priv = withTestUpdateKey(t)
	newReleaseServer(t, version, asset, priv, releaseServerOpts{})
	binPath, err := ensureToolchainBinary(&bytes.Buffer{}, version)
	if err != nil {
		t.Fatalf("seed the store with %s: %v", version, err)
	}
	return home, binPath, priv
}

func cutTheNetwork(t *testing.T) {
	t.Helper()
	prev := updateBaseURL
	updateBaseURL = "http://127.0.0.1:1"
	t.Cleanup(func() { updateBaseURL = prev })
}

func TestToolchainStoreStaysPrivate(t *testing.T) {
	home, binPath, _ := seedToolchainStore(t, "v9.9.9", releaseFixture("v9.9.9"))

	assertMode(t, home, 0o700)
	assertMode(t, filepath.Join(home, "toolchains"), 0o700)
	assertMode(t, filepath.Dir(binPath), 0o700)
	assertMode(t, binPath, 0o700)
	assertMode(t, filepath.Join(filepath.Dir(binPath), "SHA256SUMS"), 0o600)
	assertMode(t, filepath.Join(filepath.Dir(binPath), "SHA256SUMS.sig"), 0o600)
}

func TestEnsureToolchainBinaryRefetchesOnDigestMismatch(t *testing.T) {
	asset := releaseFixture("v9.9.9")
	_, binPath, _ := seedToolchainStore(t, "v9.9.9", asset)
	if err := os.WriteFile(binPath, []byte("TAMPERED"), 0o700); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := ensureToolchainBinary(&out, "v9.9.9"); err != nil {
		t.Fatalf("re-fetch after tampering: %v", err)
	}
	body, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, asset) {
		t.Fatal("a tampered cached binary survived instead of being replaced")
	}
	if !strings.Contains(out.String(), "fetched and verified") {
		t.Fatalf("tampered cache did not re-fetch: %q", out.String())
	}
}

func TestEnsureToolchainBinaryRefusesAnUnsignedManifest(t *testing.T) {
	_, binPath, _ := seedToolchainStore(t, "v9.9.9", releaseFixture("v9.9.9"))
	sig := filepath.Join(filepath.Dir(binPath), "SHA256SUMS.sig")
	if err := os.WriteFile(sig, []byte("not a signature"), 0o600); err != nil {
		t.Fatal(err)
	}
	cutTheNetwork(t)

	if _, err := ensureToolchainBinary(&bytes.Buffer{}, "v9.9.9"); err == nil {
		t.Fatal("a store whose manifest carries no trusted signature was accepted")
	}
}

// A digest sidecar with no signed manifest is what a local writer can forge; it
// must not be enough to make the store trust a binary.
func TestEnsureToolchainBinaryRefusesALocalDigestSidecar(t *testing.T) {
	_, binPath, _ := seedToolchainStore(t, "v9.9.9", releaseFixture("v9.9.9"))
	dir := filepath.Dir(binPath)
	digest, err := sha256OfFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "SHA256SUMS")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath+".sha256", []byte(digest+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cutTheNetwork(t)

	if _, err := ensureToolchainBinary(&bytes.Buffer{}, "v9.9.9"); err == nil {
		t.Fatal("a self-asserted digest sidecar was accepted as the store's trust anchor")
	}
}

// TestToolchainExecHelper is the child half of TestRunToolchainExecsTheStoredCLI:
// it execs the toolchain binary, which replaces this process.
func TestToolchainExecHelper(t *testing.T) {
	key := os.Getenv(toolchainExecHelperEnv)
	if key == "" {
		t.Skip("child half of TestRunToolchainExecsTheStoredCLI")
	}
	raw, decodeErr := hex.DecodeString(key)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	updateVerifyKey = ed25519.PublicKey(raw)
	err := runToolchain(os.Stderr, toolchainDecision{action: toolchainSwitch, installed: "v0.38.2", pin: "v9.9.9"})
	fmt.Fprintln(os.Stderr, "exec returned:", err)
	os.Exit(3)
}
