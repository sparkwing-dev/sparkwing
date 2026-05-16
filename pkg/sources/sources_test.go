package sources_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/sources"
)

func withXDG(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "sparkwing"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return tmp
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoad_RoundTripsAllTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	writeFile(t, path, `
default: team-vault
sources:
  team-vault:
    type: remote-controller
    controller: shared
  prod-vault:
    type: remote-controller
    controller: prod
  local-keychain:
    type: macos-keychain
    service: sparkwing-pi
  dotenv:
    type: file
    path: .sparkwing/secrets.local.env
  shell-env:
    type: env
    prefix: SW_
`)
	f, err := sources.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Default != "team-vault" {
		t.Errorf("Default = %q", f.Default)
	}
	if got := len(f.Sources); got != 5 {
		t.Fatalf("loaded %d sources, want 5", got)
	}
	if f.Sources["team-vault"].Controller != "shared" {
		t.Errorf("team-vault.Controller = %q", f.Sources["team-vault"].Controller)
	}
	if f.Sources["local-keychain"].Service != "sparkwing-pi" {
		t.Errorf("local-keychain.Service = %q", f.Sources["local-keychain"].Service)
	}
	if f.Sources["dotenv"].Path != ".sparkwing/secrets.local.env" {
		t.Errorf("dotenv.Path = %q", f.Sources["dotenv"].Path)
	}
	if f.Sources["shell-env"].Prefix != "SW_" {
		t.Errorf("shell-env.Prefix = %q", f.Sources["shell-env"].Prefix)
	}
}

func TestLoad_MissingFileEmpty(t *testing.T) {
	f, err := sources.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Sources) != 0 {
		t.Errorf("expected empty file, got %v", f.Sources)
	}
}

func TestLoad_UnknownFieldFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	writeFile(t, path, `
sources:
  weird:
    type: env
    extra: whoops
`)
	if _, err := sources.Load(path); err == nil {
		t.Fatal("expected KnownFields error")
	}
}

func TestValidate_UnknownType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	writeFile(t, path, `
sources:
  weird:
    type: nomad-vault
`)
	if _, err := sources.Load(path); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("expected unknown-type error, got %v", err)
	}
}

func TestValidate_RemoteControllerNeedsController(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	writeFile(t, path, `
sources:
  vault:
    type: remote-controller
`)
	if _, err := sources.Load(path); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("expected controller-required, got %v", err)
	}
}

func TestValidate_MacosKeychainNeedsService(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	writeFile(t, path, `
sources:
  kc:
    type: macos-keychain
`)
	if _, err := sources.Load(path); err == nil || !strings.Contains(err.Error(), "service") {
		t.Fatalf("expected service-required, got %v", err)
	}
}

func TestValidate_FileNeedsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	writeFile(t, path, `
sources:
  dot:
    type: file
`)
	if _, err := sources.Load(path); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("expected path-required, got %v", err)
	}
}

func TestValidate_DefaultMustNameDeclaredSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	writeFile(t, path, `
default: ghost
sources:
  real:
    type: env
`)
	if _, err := sources.Load(path); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("expected default-not-declared error, got %v", err)
	}
}

func TestValidate_EnvPrefixOptional(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	writeFile(t, path, `
sources:
  shell:
    type: env
`)
	if _, err := sources.Load(path); err != nil {
		t.Fatalf("env without prefix should be valid: %v", err)
	}
}

func TestResolve_RepoOnly(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, sources.RepoConfigPath(repoDir), `
sources:
  team:
    type: remote-controller
    controller: shared
`)
	r, ok, err := sources.Resolve(repoDir, "team")
	if err != nil || !ok {
		t.Fatalf("Resolve: %v ok=%v", err, ok)
	}
	if r.Controller != "shared" || r.Name != "team" {
		t.Errorf("got %+v", r)
	}
}

func TestResolve_RepoWinsPerField(t *testing.T) {
	xdg := withXDG(t)
	writeFile(t, filepath.Join(xdg, "sparkwing", "sources.yaml"), `
sources:
  team:
    type: remote-controller
    controller: user-override
`)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, sources.RepoConfigPath(repoDir), `
sources:
  team:
    type: remote-controller
    controller: shared
`)
	r, ok, err := sources.Resolve(repoDir, "team")
	if err != nil || !ok {
		t.Fatalf("Resolve: %v ok=%v", err, ok)
	}
	if r.Controller != "shared" {
		t.Errorf("Controller = %q, want shared (repo)", r.Controller)
	}
}

func TestResolve_DefaultFallback(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, sources.RepoConfigPath(repoDir), `
default: team
sources:
  team:
    type: remote-controller
    controller: shared
`)
	r, ok, err := sources.Resolve(repoDir, "")
	if err != nil || !ok {
		t.Fatalf("Resolve(\"\"): %v ok=%v", err, ok)
	}
	if r.Name != "team" {
		t.Errorf("name = %q, want team", r.Name)
	}
}

func TestResolve_EmptyNameNoDefaultReturnsNotFound(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, sources.RepoConfigPath(repoDir), `
sources:
  team:
    type: env
`)
	_, ok, err := sources.Resolve(repoDir, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Error("expected not-found when no default and name empty")
	}
}

func TestNames_RepoAndUser(t *testing.T) {
	xdg := withXDG(t)
	writeFile(t, filepath.Join(xdg, "sparkwing", "sources.yaml"), `
sources:
  user-only:
    type: env
`)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, sources.RepoConfigPath(repoDir), `
sources:
  repo-only:
    type: env
  shared:
    type: env
`)
	got, err := sources.Names(repoDir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	set := map[string]bool{}
	for _, n := range got {
		set[n] = true
	}
	want := []string{"repo-only", "shared", "user-only"}
	for _, n := range want {
		if !set[n] {
			t.Errorf("missing %q in %v", n, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected 3 names (no dupes), got %d: %v", len(got), got)
	}
	_ = reflect.DeepEqual // keep import
}
