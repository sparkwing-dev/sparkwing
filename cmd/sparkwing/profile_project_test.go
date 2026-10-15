package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func projectAt(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	swDir := filepath.Join(dir, ".sparkwing")
	if err := os.MkdirAll(swDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(swDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(swDir, "sparkwing.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
}

const bucketProject = `
pipelines:
  - name: demo
    entrypoint: Demo

profiles:
  bucket:
    secrets: { type: none }
    state: { type: sqlite, path: /tmp/example-project.db }
`

func TestResolveProfileFlag_RefusesProjectProfile(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  laptop: { controller: { url: https://safe.example } }\n")
	projectAt(t, bucketProject)
	if p, err := resolveProfileFlag("bucket"); err == nil || p != nil {
		t.Fatalf("project supplied a connection: %#v, %v", p, err)
	}
}

func TestResolveProfileFlag_UserProfileWinsNameCollision(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  bucket: { controller: { url: https://machine.example } }\n")
	projectAt(t, bucketProject)

	p, err := resolveProfileFlag("bucket")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.ControllerURL() != "https://machine.example" {
		t.Error("project profile displaced the machine connection")
	}
}

func TestResolveProfileFlag_NotFoundNamesOnlyMachineConfig(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  laptop: { controller: { url: https://machine.example } }\n")
	projectAt(t, bucketProject)

	_, err := resolveProfileFlag("nope")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if strings.Contains(err.Error(), "sparkwing.yaml") {
		t.Errorf("message should name only machine config: %q", err.Error())
	}
}

func TestResolveProfileFlag_NoFlagSelectsNothingAProjectDeclares(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  laptop: { controller: { url: https://machine.example } }\n")
	projectAt(t, bucketProject)

	p, err := resolveProfileFlag("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p != nil {
		t.Errorf("got profile %#v, want the local store", p)
	}
	if _, err := resolveProfile(""); !errors.Is(err, errNoProfile) {
		t.Errorf("resolveProfile with no flag = %v, want ErrNoProfile", err)
	}
}

func TestResolveProfileFlag_IgnoresInvalidProjectProfile(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  laptop: { controller: { url: https://machine.example } }\n")
	projectAt(t, `
profiles:
  bucket:
    secrets: { type: none }
    state: { type: s3, bucket: example-bucket, prefix: state }
`)
	_, err := resolveProfileFlag("bucket")
	if err == nil || !strings.Contains(err.Error(), `profile "bucket" not found`) {
		t.Fatalf("resolve = %v, want missing machine connection", err)
	}
}
