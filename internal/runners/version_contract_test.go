package runners

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

type installationFixture struct {
	calls                   []string
	fail                    string
	held, renewed           int
	configuration, schedule string
}

func (f *installationFixture) step(name string) error {
	f.calls = append(f.calls, name)
	if f.fail == name {
		return fmt.Errorf("fixture %s failure", name)
	}
	return nil
}
func (f *installationFixture) Drain(context.Context) error {
	if err := f.step("drain"); err != nil {
		return err
	}
	f.renewed += f.held
	f.held = 0
	return nil
}
func (f *installationFixture) Replace(context.Context, engine.VerifiedRelease) error {
	if f.held != 0 {
		return fmt.Errorf("replaced before draining")
	}
	return f.step("replace")
}
func (f *installationFixture) RefreshManagedUnits(context.Context) error { return f.step("refresh") }
func (f *installationFixture) Restart(context.Context) error             { return f.step("restart") }
func TestSoftwareUpdateDrainsBeforeReplacingAndPreservesRegistration(t *testing.T) {
	release := runnerReleaseFixture(t)
	update := engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: "v0.20.0", Release: release, Required: true}
	for i, fail := range []string{"drain", "replace", "refresh", "restart", ""} {
		t.Run("failure_"+fail, func(t *testing.T) {
			f := &installationFixture{fail: fail, held: 2, configuration: "existing credential", schedule: "existing timer"}
			err := ApplySoftwareUpdate(context.Background(), update, f)
			if (err != nil) != (fail != "") {
				t.Fatalf("error=%v", err)
			}
			want := []string{"drain", "replace", "refresh", "restart"}
			if i < 4 {
				want = want[:i+1]
			}
			if !reflect.DeepEqual(f.calls, want) {
				t.Fatalf("calls=%v want=%v", f.calls, want)
			}
			if fail != "drain" && (f.held != 0 || f.renewed != 2) {
				t.Fatal("held claims were not renewed while draining")
			}
			if f.configuration != "existing credential" || f.schedule != "existing timer" {
				t.Fatal("configuration or schedules changed")
			}
		})
	}
}
func TestSoftwareUpdateRejectsMissingVerificationBeforeDrain(t *testing.T) {
	f := &installationFixture{}
	if err := ApplySoftwareUpdate(context.Background(), engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: "v0.20.0", Required: true}, f); err == nil {
		t.Fatal("unverified replacement accepted")
	}
	if len(f.calls) != 0 {
		t.Fatal("unverified update started draining")
	}
	if err := ApplySoftwareUpdate(context.Background(), engine.SoftwareUpdate{CurrentVersion: "v0.20.0", Version: "v0.20.0"}, f); err != nil {
		t.Fatal(err)
	}
}
func runnerReleaseFixture(t *testing.T) engine.VerifiedRelease {
	t.Helper()
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := releaseauth.TrustedPublicKeys
	releaseauth.TrustedPublicKeys = []string{base64.StdEncoding.EncodeToString(public)}
	t.Cleanup(func() { releaseauth.TrustedPublicKeys = previous })
	dir := t.TempDir()
	for path, body := range map[string]string{"go.mod": "module github.com/sparkwing-dev/sparkwing/cmd/sparkwing-runner\ngo 1.26\n", "main.go": "package main\nvar Version=\"dev\"\nfunc main(){println(Version)}\n"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); err != nil {
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
	release, err := engine.VerifyRelease("v0.20.0", target, engine.ReleaseBundle{Asset: body, AssetSignature: ed25519.Sign(key, body), Manifest: manifest, ManifestSignature: ed25519.Sign(key, manifest)})
	if err != nil {
		t.Fatal(err)
	}
	return release
}
