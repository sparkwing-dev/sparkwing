package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func engineExecutable(t *testing.T, version, arch string) []byte {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module "+engine.HostModule+"\ngo 1.26\n")
	write("cmd/sparkwing/main.go", "package main\nvar Version=\"dev\"\nfunc main(){println(Version)}\n")
	out := filepath.Join(dir, "sparkwing")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.Version="+version, "-o", out, "./cmd/sparkwing")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-buildvcs=false")
	if body, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture engine: %v\n%s", err, body)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signedEngineBundle(t *testing.T, key ed25519.PrivateKey, version string, executables map[string][]byte) string {
	t.Helper()
	release := filepath.Join(t.TempDir(), version)
	if err := os.MkdirAll(release, 0o700); err != nil {
		t.Fatal(err)
	}
	names, err := releaseasset.NamesForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	var lines []string
	for _, name := range names {
		body := []byte(name)
		for arch, executable := range executables {
			if name == "sparkwing-linux-"+arch {
				body = executable
				files[name] = executable
				files[name+".sig"] = ed25519.Sign(key, executable)
			}
		}
		sum := sha256.Sum256(body)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+name)
	}
	files["SHA256SUMS"] = []byte(strings.Join(lines, "\n") + "\n")
	files["SHA256SUMS.sig"] = ed25519.Sign(key, files["SHA256SUMS"])
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(release, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return release
}

func TestEngineBundleVerifiesLikeTheRunner(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := []ed25519.PublicKey{public}
	const version = "v0.10.2"
	executables := map[string][]byte{"amd64": engineExecutable(t, version, "amd64"), "arm64": engineExecutable(t, version, "arm64")}
	if err := verifyEngineBundle(signedEngineBundle(t, private, version, executables), keys); err != nil {
		t.Fatalf("a correctly signed bundle failed verification: %v", err)
	}
	_, other, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyEngineBundle(signedEngineBundle(t, other, version, executables), keys); err == nil {
		t.Fatal("verified a bundle signed by an untrusted key")
	}
	for _, file := range []string{"SHA256SUMS.sig", "sparkwing-linux-arm64.sig"} {
		release := signedEngineBundle(t, private, version, executables)
		signature, err := os.ReadFile(filepath.Join(release, file))
		if err != nil {
			t.Fatal(err)
		}
		signature[0] ^= 0xff
		if err := os.WriteFile(filepath.Join(release, file), signature, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyEngineBundle(release, keys); err == nil {
			t.Fatalf("verified a bundle with a corrupt %s", file)
		}
	}
	mislabeled := map[string][]byte{"amd64": executables["amd64"], "arm64": engineExecutable(t, "v0.10.1", "arm64")}
	if err := verifyEngineBundle(signedEngineBundle(t, private, version, mislabeled), keys); err == nil {
		t.Fatal("verified a signed executable whose embedded version differs from the release")
	}
	swapped := map[string][]byte{"amd64": executables["arm64"], "arm64": executables["amd64"]}
	if err := verifyEngineBundle(signedEngineBundle(t, private, version, swapped), keys); err == nil {
		t.Fatal("verified a signed executable built for another platform")
	}
	if err := run([]string{"--engine-bundle", signedEngineBundle(t, private, version, executables)}); err == nil {
		t.Fatal("the command accepted a bundle the release keys did not sign")
	}
}
