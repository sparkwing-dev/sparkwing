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

// withTestUpdateKey swaps the embedded verification key for a freshly
// generated test key and returns the matching private key. The real
// (placeholder) key is restored when the test ends -- production code
// never sees the test key.
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

// releaseServerOpts corrupts a served release to drive the failure paths.
type releaseServerOpts struct {
	sumsDigest string             // override the digest listed for the asset
	signWith   ed25519.PrivateKey // sign SHA256SUMS with this key instead
	omitSig    bool               // do not serve SHA256SUMS.sig (404)
	omitAsset  bool               // do not serve the asset (404)
}

// newReleaseServer serves a version's asset, SHA256SUMS, and its ed25519
// detached signature, applying any corruption in opts. It points
// updateBaseURL at itself for the duration of the test.
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

	mux := http.NewServeMux()
	mux.HandleFunc("/"+version+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		if opts.omitAsset {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(assetBytes)
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

// writeCurrentBin creates a stand-in for the running binary with known
// prior bytes so replacement / restoration is observable.
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

// Contract 1-4 + 6, happy path: a valid signed asset installs atomically,
// the installed bytes equal the served bytes (no post-verify mutation),
// and the installed digest equals the signed manifest digest.
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
	// Installed bytes are byte-identical to the served/verified bytes:
	// proves no codesign or any other mutation happened after verification.
	if !bytesEqual(got, newBytes) {
		t.Fatalf("installed bytes differ from served bytes: got %d bytes, want %d", len(got), len(newBytes))
	}
	if mustSHA256(got) != res.digest {
		t.Errorf("installed digest %q != verified digest %q", mustSHA256(got), res.digest)
	}
	// No staging/backup litter left behind on success.
	assertAbsent(t, currentBin+".update.tmp")
	assertAbsent(t, currentBin+".prev")
}

// Contract 1 + no-replacement: a bad signature fails and does not replace
// the current binary.
func TestDownloadAndInstall_BadSignature_NoReplacement(t *testing.T) {
	priv := withTestUpdateKey(t)
	_, wrongKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newBytes := []byte("ATTACKER-CONTROLLED-BYTES")
	// Manifest is signed by a key that is NOT the embedded one.
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

// Contract 1 + no-replacement: a missing signature (404) fails without
// replacement -- fail closed, exactly the placeholder-key/unarmed case.
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

// Contract 4 + no-replacement: a digest mismatch (signature valid over a
// manifest that lists the wrong digest) fails without replacement.
func TestDownloadAndInstall_DigestMismatch_NoReplacement(t *testing.T) {
	priv := withTestUpdateKey(t)
	newBytes := []byte("BYTES-THAT-DO-NOT-MATCH-THE-LISTED-DIGEST")
	wrong := strings.Repeat("00", sha256.Size) // valid-length, wrong digest
	newReleaseServer(t, "v9.9.9", newBytes, priv, releaseServerOpts{sumsDigest: wrong})
	old := []byte("OLD-BINARY-BYTES")
	currentBin := writeCurrentBin(t, old)

	_, err := downloadAndInstall("v9.9.9", currentBin)
	if err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error did not mention the checksum mismatch: %v", err)
	}
	assertBytes(t, currentBin, old)
}

// The post-rename digest recheck: if the installed bytes are corrupted
// after the atomic swap, the updater restores the prior binary and fails
// loudly. Simulated via the afterInstallHook seam.
func TestDownloadAndInstall_PostRenameMismatch_RestoresPrior(t *testing.T) {
	priv := withTestUpdateKey(t)
	newBytes := []byte("GOOD-VERIFIED-RELEASE-BYTES")
	newReleaseServer(t, "v9.9.9", newBytes, priv, releaseServerOpts{})
	old := []byte("KNOWN-GOOD-PRIOR-BINARY")
	currentBin := writeCurrentBin(t, old)

	afterInstallHook = func(installed string) {
		// Corrupt the just-installed file so the re-hash cannot match.
		_ = os.WriteFile(installed, []byte("CORRUPTED-AFTER-RENAME"), 0o755)
	}
	t.Cleanup(func() { afterInstallHook = nil })

	_, err := downloadAndInstall("v9.9.9", currentBin)
	if err == nil {
		t.Fatal("post-rename corruption was not detected")
	}
	if !strings.Contains(err.Error(), "installed bytes do not match") {
		t.Errorf("error did not describe the post-install mismatch: %v", err)
	}
	// The prior binary is restored, not left corrupted.
	assertBytes(t, currentBin, old)
	assertAbsent(t, currentBin+".prev")
}

// Contract 5, via a PATH spy independent of code structure: a
// download/verification failure spawns no `go` subprocess at all -- the
// `go install` fallback is gone.
func TestRunUpdateBinary_Failure_SpawnsNoGo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim spy is POSIX-only")
	}
	priv := withTestUpdateKey(t)
	// Serve a valid manifest+sig but 404 the asset, so download fails.
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

// --- small assertion helpers ---

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
