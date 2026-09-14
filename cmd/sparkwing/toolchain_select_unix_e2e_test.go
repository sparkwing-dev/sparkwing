//go:build e2e && !windows

package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestEnsureToolchainBinaryAnnouncesARejectedStoreBeforeRefetching(t *testing.T) {
	asset := releaseFixture("v9.9.9")
	_, binPath, _ := seedToolchainStore(t, "v9.9.9", asset)
	if err := os.WriteFile(binPath, []byte("TAMPERED"), 0o700); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := ensureToolchainBinary(&out, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "stored toolchain v9.9.9 failed verification (") {
		t.Errorf("a rejected store was replaced silently: %q", out.String())
	}
	if !strings.Contains(out.String(), "fetching again") {
		t.Errorf("notice %q does not say what it did about it", out.String())
	}
}

func TestToolchainFetchErrorCarriesTheRejectedStoresReason(t *testing.T) {
	_, binPath, _ := seedToolchainStore(t, "v9.9.9", releaseFixture("v9.9.9"))
	if err := os.WriteFile(binPath, []byte("TAMPERED"), 0o700); err != nil {
		t.Fatal(err)
	}
	cutTheNetwork(t)

	_, err := ensureToolchainBinary(&bytes.Buffer{}, "v9.9.9")
	if err == nil {
		t.Fatal("a tampered store with no network produced no error")
	}
	for _, want := range []string{"already in the store was rejected", "digest", "sparkwing update --version v9.9.9"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestEnsureToolchainBinaryDropsAPreFixDigestSidecar(t *testing.T) {
	_, binPath, _ := seedToolchainStore(t, "v9.9.9", releaseFixture("v9.9.9"))
	orphan := binPath + ".sha256"
	if err := os.WriteFile(orphan, []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cutTheNetwork(t)

	if _, err := ensureToolchainBinary(&bytes.Buffer{}, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("the pre-fix digest sidecar survived: %v", err)
	}
}

func TestAssertToolchainVersionReportsWhatTheChildPrinted(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fresh-home")
	t.Setenv("SPARKWING_HOME", home)
	priv := withTestUpdateKey(t)
	rejecting := []byte("#!/bin/sh\necho \"unknown flag: --offline\" >&2\nexit 2\n")
	newReleaseServer(t, "v9.9.9", rejecting, priv, releaseServerOpts{})

	_, err := ensureToolchainBinary(&bytes.Buffer{}, "v9.9.9")
	if err == nil {
		t.Fatal("a release that rejects the version query was cached")
	}
	for _, want := range []string{"version -o json --offline", "unknown flag: --offline", "exit status 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}
