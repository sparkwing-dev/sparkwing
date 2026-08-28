package main

import (
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

defaults:
  profile: bucket

profiles:
  bucket:
    secrets: { type: none }
    state: { type: s3, bucket: example-bucket, prefix: state }
    logs: { type: s3, bucket: example-bucket, prefix: logs }
    cache: { type: s3, bucket: example-bucket, prefix: cache }
`

func TestResolveProfileFlag_FindsProjectProfile(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  laptop: { state: { type: sqlite } }\n")
	projectAt(t, bucketProject)

	p, err := resolveProfileFlag("bucket")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	state, _, _ := p.SurfaceStrings()
	if !strings.Contains(state, "example-bucket") {
		t.Errorf("state surface: got %q, want the project profile's bucket", state)
	}
}

func TestResolveProfileFlag_UserProfileWinsNameCollision(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  bucket: { state: { type: sqlite, path: /tmp/user.db } }\n")
	projectAt(t, bucketProject)

	p, err := resolveProfileFlag("bucket")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	state, _, _ := p.SurfaceStrings()
	if !strings.Contains(state, "user.db") {
		t.Errorf("state surface: got %q, want the user profile", state)
	}
}

func TestResolveProfileFlag_NotFoundNamesBothNamespaces(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  laptop: { state: { type: sqlite } }\n")
	projectAt(t, bucketProject)

	_, err := resolveProfileFlag("nope")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "sparkwing.yaml") {
		t.Errorf("message should say the project file was checked too: %q", err.Error())
	}
}

func TestResolveProfileChain_NoFlagUsesProjectDefault(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  laptop: { state: { type: sqlite } }\n")
	projectAt(t, bucketProject)

	p, chain, _, err := resolveProfileChain("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p == nil {
		t.Fatal("no-flag resolution returned no profile despite defaults.profile: bucket")
	}
	if chain.Selected != "bucket" {
		t.Errorf("Selected: got %q, want bucket", chain.Selected)
	}
	state, _, _ := p.SurfaceStrings()
	if !strings.Contains(state, "example-bucket") {
		t.Errorf("state surface: got %q, want the project default's bucket", state)
	}
}

func TestResolveProfileChain_NoFlagNoProjectDefault(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  laptop: { state: { type: sqlite } }\n")
	projectAt(t, "pipelines:\n  - name: demo\n    entrypoint: Demo\n")

	p, _, _, err := resolveProfileChain("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p != nil {
		t.Errorf("got profile %#v, want none", p)
	}
}
