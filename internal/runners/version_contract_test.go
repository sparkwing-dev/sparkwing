package runners

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

type installationFixture struct {
	calls []string
}

func (f *installationFixture) Apply(context.Context, engine.VerifiedRelease) error {
	f.calls = append(f.calls, "apply")
	return nil
}

func TestSoftwareUpdateRejectsMissingVerificationBeforeDrain(t *testing.T) {
	f := &installationFixture{}
	if err := applySoftwareUpdate(context.Background(), engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: "v0.20.0", Required: true}, f); err == nil {
		t.Fatal("unverified replacement accepted")
	}
	if len(f.calls) != 0 {
		t.Fatal("unverified update started draining")
	}
	if err := applySoftwareUpdate(context.Background(), engine.SoftwareUpdate{CurrentVersion: "v0.20.0", Version: "v0.20.0"}, f); err != nil {
		t.Fatal(err)
	}
}

func runnerReleaseFixture(t *testing.T) engine.VerifiedRelease {
	t.Helper()
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for path, body := range map[string]string{"go.mod": "module github.com/sparkwing-dev/sparkwing/cmd/sparkwing-runner\ngo 1.26\n", "main.go": "package main\nvar Version=\"dev\"\nfunc main(){println(Version)}\n"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(dir, "fixture")
	command := exec.Command("go", "build", "-p", "2", "-ldflags", "-X main.Version=v0.20.0", "-o", executable, ".")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false", "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, output)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	target := releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	name, err := target.NameForVersion("v0.20.0")
	if err != nil {
		t.Fatal(err)
	}
	names, err := releaseasset.NamesForVersion("v0.20.0")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, item := range names {
		hash := sha256.Sum256([]byte(item))
		if item == name {
			hash = sha256.Sum256(body)
		}
		lines = append(lines, fmt.Sprintf("%x  %s", hash, item))
	}
	manifest := []byte(strings.Join(lines, "\n") + "\n")
	release, err := engine.VerifyRelease("v0.20.0", target, engine.ReleaseBundle{Asset: body, AssetSignature: ed25519.Sign(key, body), Manifest: manifest, ManifestSignature: ed25519.Sign(key, manifest)}, []ed25519.PublicKey{public})
	if err != nil {
		t.Fatal(err)
	}
	return release
}
