//go:build !windows

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const toolchainExecHelperEnv = "SPARKWING_TOOLCHAIN_EXEC_HELPER"

// releaseFixture is a stand-in release asset. It answers `version -o json` with
// the version it was published as, which is what the store's writer checks, and
// echoes anything else so a test can see the argv the exec handed it.
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

func TestEnsureToolchainBinaryFetchesVerifiesAndCaches(t *testing.T) {
	asset := releaseFixture("v9.9.9")
	home, binPath, _ := seedToolchainStore(t, "v9.9.9", asset)

	want := filepath.Join(home, "toolchains", "v9.9.9", "sparkwing")
	if binPath != want {
		t.Fatalf("store path = %q, want %q", binPath, want)
	}
	body, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, asset) {
		t.Fatal("stored binary does not match the verified release asset")
	}

	cutTheNetwork(t)
	var out bytes.Buffer
	if _, err := ensureToolchainBinary(&out, "v9.9.9"); err != nil {
		t.Fatalf("cache hit reached the network: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("cache hit printed %q, want nothing", out.String())
	}
}

func TestEnsureToolchainBinaryAnnouncesTheFetchItVerified(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fresh-home")
	t.Setenv("SPARKWING_HOME", home)
	priv := withTestUpdateKey(t)
	asset := releaseFixture("v9.9.9")
	newReleaseServer(t, "v9.9.9", asset, priv, releaseServerOpts{})

	var out bytes.Buffer
	if _, err := ensureToolchainBinary(&out, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fetched and verified sparkwing v9.9.9", mustSHA256(asset)} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("fetch notice %q does not contain %q", out.String(), want)
		}
	}
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

// A permission sweep over the sparkwing home clamps regular files to 0600. The
// store must heal that without refetching the release.
func TestEnsureToolchainBinaryRestoresAStrippedExecuteBit(t *testing.T) {
	_, binPath, _ := seedToolchainStore(t, "v9.9.9", releaseFixture("v9.9.9"))
	if err := os.Chmod(binPath, 0o600); err != nil {
		t.Fatal(err)
	}
	cutTheNetwork(t)

	var out bytes.Buffer
	if _, err := ensureToolchainBinary(&out, "v9.9.9"); err != nil {
		t.Fatalf("a stored binary that lost its execute bit was not healed: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("healing the mode went to the network: %q", out.String())
	}
	assertMode(t, binPath, 0o700)
}

func TestEnsureToolchainBinaryRefusesAReleaseThatReportsAnotherVersion(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fresh-home")
	t.Setenv("SPARKWING_HOME", home)
	priv := withTestUpdateKey(t)
	newReleaseServer(t, "v9.9.9", releaseFixture("v0.1.0"), priv, releaseServerOpts{})

	_, err := ensureToolchainBinary(&bytes.Buffer{}, "v9.9.9")
	if err == nil {
		t.Fatal("a release that identifies as another version was cached")
	}
	for _, want := range []string{"v9.9.9", "v0.1.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(home, "toolchains", "v9.9.9", "sparkwing")); statErr == nil {
		t.Error("the mismatched release was cached anyway")
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

func TestRunToolchainExecsTheStoredCLI(t *testing.T) {
	home, binPath, _ := seedToolchainStore(t, "v9.9.9", releaseFixture("v9.9.9"))

	// #nosec G702 -- the test binary re-running one of its own tests as the child half
	cmd := exec.Command(os.Args[0], "-test.run=^TestToolchainExecHelper$")
	cmd.Env = append(os.Environ(),
		toolchainExecHelperEnv+"="+hex.EncodeToString(updateVerifyKey),
		"SPARKWING_HOME="+home)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run child: %v (stderr %s)", err, stderr.String())
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7 from the fixture (stdout %q, stderr %q)", code, out, stderr.String())
	}
	if !strings.Contains(string(out), "fixture argv: -test.run=^TestToolchainExecHelper$") {
		t.Errorf("the toolchain did not receive the original argv: %q", out)
	}
	if !strings.Contains(string(out), "fixture active: v9.9.9") {
		t.Errorf("the toolchain did not receive the recursion guard: %q", out)
	}
	notice := "sparkwing: running v9.9.9 from " + tildePath(binPath) +
		" because this repo pins SDK v9.9.9 and the installed sparkwing is v0.38.2"
	if !strings.Contains(stderr.String(), notice) {
		t.Errorf("stderr %q does not carry the switch notice %q", stderr.String(), notice)
	}
	if strings.Contains(string(out), "sparkwing: running") {
		t.Error("the switch notice reached stdout")
	}
}
