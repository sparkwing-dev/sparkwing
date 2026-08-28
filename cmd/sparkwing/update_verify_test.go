package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func withTestUpdateKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	prev := updateVerifyKey
	updateVerifyKey = pub
	t.Cleanup(func() { updateVerifyKey = prev })
	return priv
}

func expectedAssetName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return "sparkwing-" + runtime.GOOS + "-" + runtime.GOARCH + ext
}

type releaseServerOpts struct {
	sumsDigest string
	signWith   ed25519.PrivateKey
	omitSig    bool
	omitAsset  bool
}

func newReleaseServer(t *testing.T, version string, assetBytes []byte, signKey ed25519.PrivateKey, opts releaseServerOpts) {
	t.Helper()
	asset := expectedAssetName()

	realDigest := sha256.Sum256(assetBytes)
	digest := hex.EncodeToString(realDigest[:])
	if opts.sumsDigest != "" {
		digest = opts.sumsDigest
	}
	sums := []byte(digest + "  " + asset + "\n")

	key := signKey
	if opts.signWith != nil {
		key = opts.signWith
	}
	sig := ed25519.Sign(key, sums)
	assetSig := ed25519.Sign(key, assetBytes)

	mux := http.NewServeMux()
	mux.HandleFunc("/"+version+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		if opts.omitAsset {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(assetBytes)
	})
	mux.HandleFunc("/"+version+"/"+asset+".sig", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(assetSig)
	})
	mux.HandleFunc("/"+version+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sums)
	})
	mux.HandleFunc("/"+version+"/SHA256SUMS.sig", func(w http.ResponseWriter, r *http.Request) {
		if opts.omitSig {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(sig)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	prev := updateBaseURL
	updateBaseURL = srv.URL
	t.Cleanup(func() { updateBaseURL = prev })
}

func writeCurrentBin(t *testing.T, bytes []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "sparkwing-current")
	if err := os.WriteFile(p, bytes, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestDownloadAndInstall_ValidSignedAsset(t *testing.T) {
	priv := withTestUpdateKey(t)
	newBytes := []byte("SPARKWING-NEW-RELEASE-BYTES-v9.9.9\x00\x01\x02")
	newReleaseServer(t, "v9.9.9", newBytes, priv, releaseServerOpts{})
	currentBin := writeCurrentBin(t, []byte("OLD-BINARY-BYTES"))

	res, err := downloadAndInstall("v9.9.9", currentBin)
	if err != nil {
		t.Fatalf("valid signed install failed: %v", err)
	}
	if res.path != currentBin {
		t.Errorf("result path = %q, want %q", res.path, currentBin)
	}
	if res.version != "v9.9.9" {
		t.Errorf("result version = %q, want v9.9.9", res.version)
	}
	if res.digest != mustSHA256(newBytes) {
		t.Errorf("result digest = %q, want %q", res.digest, mustSHA256(newBytes))
	}

	got, err := os.ReadFile(currentBin)
	if err != nil {
		t.Fatal(err)
	}

	if !bytesEqual(got, newBytes) {
		t.Fatalf("installed bytes differ from served bytes: got %d bytes, want %d", len(got), len(newBytes))
	}
	if mustSHA256(got) != res.digest {
		t.Errorf("installed digest %q != verified digest %q", mustSHA256(got), res.digest)
	}

	assertAbsent(t, currentBin+".update.tmp")
	assertAbsent(t, currentBin+".prev")
}

func TestDownloadAndInstall_BadSignature_NoReplacement(t *testing.T) {
	priv := withTestUpdateKey(t)
	_, wrongKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newBytes := []byte("ATTACKER-CONTROLLED-BYTES")

	newReleaseServer(t, "v9.9.9", newBytes, priv, releaseServerOpts{signWith: wrongKey})
	old := []byte("OLD-BINARY-BYTES")
	currentBin := writeCurrentBin(t, old)

	_, err = downloadAndInstall("v9.9.9", currentBin)
	if err == nil {
		t.Fatal("bad signature was accepted")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("error did not mention the signature: %v", err)
	}
	assertBytes(t, currentBin, old)
	assertAbsent(t, currentBin+".prev")
}

func TestDownloadAndInstall_MissingSignature_NoReplacement(t *testing.T) {
	priv := withTestUpdateKey(t)
	newBytes := []byte("SOME-BYTES")
	newReleaseServer(t, "v9.9.9", newBytes, priv, releaseServerOpts{omitSig: true})
	old := []byte("OLD-BINARY-BYTES")
	currentBin := writeCurrentBin(t, old)

	if _, err := downloadAndInstall("v9.9.9", currentBin); err == nil {
		t.Fatal("missing signature was accepted")
	}
	assertBytes(t, currentBin, old)
}

func TestDownloadAndInstall_DigestMismatch_NoReplacement(t *testing.T) {
	priv := withTestUpdateKey(t)
	newBytes := []byte("BYTES-THAT-DO-NOT-MATCH-THE-LISTED-DIGEST")
	wrong := strings.Repeat("00", sha256.Size)
	newReleaseServer(t, "v9.9.9", newBytes, priv, releaseServerOpts{sumsDigest: wrong})
	old := []byte("OLD-BINARY-BYTES")
	currentBin := writeCurrentBin(t, old)

	_, err := downloadAndInstall("v9.9.9", currentBin)
	if err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if !strings.Contains(err.Error(), "signatures do not match") {
		t.Errorf("error did not mention the checksum mismatch: %v", err)
	}
	assertBytes(t, currentBin, old)
}

func TestDownloadAndInstall_PostRenameMismatch_RestoresPrior(t *testing.T) {
	priv := withTestUpdateKey(t)
	newBytes := []byte("GOOD-VERIFIED-RELEASE-BYTES")
	newReleaseServer(t, "v9.9.9", newBytes, priv, releaseServerOpts{})
	old := []byte("KNOWN-GOOD-PRIOR-BINARY")
	currentBin := writeCurrentBin(t, old)

	previousHook := afterInstallHook
	afterInstallHook = func(installed string) {

		_ = os.WriteFile(installed, []byte("CORRUPTED-AFTER-RENAME"), 0o755)
	}
	t.Cleanup(func() { afterInstallHook = previousHook })

	_, err := downloadAndInstall("v9.9.9", currentBin)
	if err == nil {
		t.Fatal("post-rename corruption was not detected")
	}
	if !strings.Contains(err.Error(), "installed binary digest mismatch") {
		t.Errorf("error did not describe the post-install mismatch: %v", err)
	}

	assertBytes(t, currentBin, old)
	assertAbsent(t, currentBin+".prev")
}

func TestRunUpdateBinary_Failure_SpawnsNoGo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim spy is POSIX-only")
	}
	priv := withTestUpdateKey(t)

	newReleaseServer(t, "v9.9.9", []byte("unused"), priv, releaseServerOpts{omitAsset: true})

	shimDir := t.TempDir()
	spyLog := filepath.Join(shimDir, "go-invocations.log")
	shim := "#!/bin/sh\necho \"$@\" >> " + spyLog + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir)

	err := runUpdateBinary("v9.9.9", false, false)
	if err == nil {
		t.Fatal("download failure was not terminal")
	}
	if _, statErr := os.Stat(spyLog); statErr == nil {
		data, _ := os.ReadFile(spyLog)
		t.Fatalf("a `go` subprocess was spawned on the failure path:\n%s", data)
	}
}

func TestDownloadAndInstall_PlaceholderKey_FailsClosed(t *testing.T) {
	prev := updateVerifyKey
	updateVerifyKey = make(ed25519.PublicKey, ed25519.PublicKeySize)
	t.Cleanup(func() { updateVerifyKey = prev })
	if !isPlaceholderUpdateKey(updateVerifyKey) {
		t.Fatal("test setup: injected key is not the placeholder")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	newReleaseServer(t, "v9.9.9", []byte("whatever"), priv, releaseServerOpts{})
	old := []byte("OLD-BINARY-BYTES")
	currentBin := writeCurrentBin(t, old)

	_, err = downloadAndInstall("v9.9.9", currentBin)
	if err == nil {
		t.Fatal("placeholder build installed a release")
	}
	if !strings.Contains(err.Error(), "not armed") {
		t.Errorf("error did not explain the build is unarmed: %v", err)
	}
	assertBytes(t, currentBin, old)
}

func TestDownloadFile_BoundsBodySize(t *testing.T) {
	var size int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, size))
	}))
	t.Cleanup(srv.Close)
	dst := filepath.Join(t.TempDir(), "out")

	size = 2000
	if err := downloadFile(srv.URL, dst, 1000); err == nil {
		t.Fatal("oversized body was accepted")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error did not name the size limit: %v", err)
	}

	size = 1000
	if err := downloadFile(srv.URL, dst, 1000); err != nil {
		t.Fatalf("at-limit body was rejected: %v", err)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytesEqual(got, want) {
		t.Fatalf("%s was modified: got %q, want %q", path, got, want)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected %s to be absent, but it exists", path)
	}
}
