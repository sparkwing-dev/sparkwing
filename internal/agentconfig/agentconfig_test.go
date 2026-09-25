package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestConfig_RoundTripFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	yaml := `
controller: http://localhost:4344
logs: http://localhost:4345
gitcache: http://localhost:4344/api/v1/gitcache
profile: dev
token: tok-abc
max_concurrent: 3
labels:
  - laptop
  - arch=arm64
  - "  "
spawn_policy: return-to-queue
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Controller != "http://localhost:4344" {
		t.Fatalf("controller: %q", cfg.Controller)
	}
	if cfg.Gitcache != "http://localhost:4344/api/v1/gitcache" {
		t.Fatalf("gitcache url=%q", cfg.Gitcache)
	}
	if cfg.Token != "tok-abc" || cfg.MaxConcurrent != 3 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
	norm, err := Validate(*cfg)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(norm.Labels) != 2 || norm.Labels[0] != "laptop" || norm.Labels[1] != "arch=arm64" {
		t.Fatalf("labels normalization: %v", norm.Labels)
	}
	if norm.Poll <= 0 || norm.Lease <= 0 {
		t.Fatalf("defaults missing: %+v", norm)
	}
}

func TestConfig_RejectsUnknownFieldsAndAdditionalDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"unknown field", "controller: http://localhost:4344\nadmin: true\n", "field admin not found"},
		{"second document", "controller: http://localhost:4344\n---\ntoken: hidden\n", "multiple YAML documents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := fssecure.SecurePrivateConfig(path); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConfig_RejectsMissingController(t *testing.T) {
	_, err := Validate(Config{Token: "x"})
	if err == nil {
		t.Fatal("expected error for missing controller")
	}
}

func TestConfig_RejectsUnsupportedSpawnPolicy(t *testing.T) {
	for _, policy := range []string{"run-local", "auto", "bogus"} {
		_, err := Validate(Config{
			Controller:  "http://x",
			SpawnPolicy: policy,
		})
		if err == nil {
			t.Fatalf("spawn_policy=%q should be rejected", policy)
		}
	}
}

func TestConfig_DefaultsSpawnPolicy(t *testing.T) {
	norm, err := Validate(Config{Controller: "http://x"})
	if err != nil {
		t.Fatal(err)
	}
	if norm.SpawnPolicy != "return-to-queue" {
		t.Fatalf("default: %q", norm.SpawnPolicy)
	}
	if norm.Gitcache != "http://x/api/v1/gitcache" {
		t.Fatalf("gitcache default = %q, want controller proxy", norm.Gitcache)
	}
	if norm.LocalAdmission == nil || *norm.LocalAdmission {
		t.Fatal("legacy singular config did not preserve disabled local admission")
	}
}

func TestConfig_PreservesADisabledLocalAdmissionChoice(t *testing.T) {
	disabled := false
	cfg, err := Validate(Config{Controller: "http://x", LocalAdmission: &disabled})
	if err != nil || cfg.LocalAdmission == nil || *cfg.LocalAdmission {
		t.Fatalf("local_admission:false = %+v, %v", cfg.LocalAdmission, err)
	}
	enabled := true
	cfg, err = Validate(Config{Controller: "http://x", LocalAdmission: &enabled})
	if err != nil || cfg.LocalAdmission == nil || !*cfg.LocalAdmission {
		t.Fatalf("local_admission:true = %+v, %v", cfg.LocalAdmission, err)
	}
}

func TestLoad_RefusesTheRemovedEnrolledKeys(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"name", "controller: http://localhost:4344\nname: desk\ntoken: tok\n"},
		{"coordinators", "coordinators:\n  - controller: http://localhost:4344\n    token: tok\n"},
		{"both", "name: desk\ncoordinators:\n  - controller: http://localhost:4344\n    token: tok\n"},
		{"capitalized name", "controller: http://localhost:4344\nName: desk\ntoken: tok\n"},
		{"merge key", "base: &b\n  name: desk\ncontroller: http://localhost:4344\ntoken: tok\n<<: *b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePrivateConfig(t, tc.body)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), EnrolledModeRemoved) {
				t.Fatalf("Load error = %v, want %q", err, EnrolledModeRemoved)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("refusal does not name the file: %v", err)
			}
		})
	}
}

func TestLoad_KeepsEveryClaimModeSetting(t *testing.T) {
	path := writePrivateConfig(t, `controller: http://localhost:4344
logs: http://localhost:4345
token: tok-abc
holder_prefix: dev-laptop
max_concurrent: 2
contribution: 4,8gb
local_admission: true
local_reserve: 1,2gb
labels:
  - linux
`)
	raw, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg, err := Validate(*raw)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Controller != "http://localhost:4344" || cfg.Logs != "http://localhost:4345" ||
		cfg.Token != "tok-abc" || cfg.HolderPrefix != "dev-laptop" || cfg.MaxConcurrent != 2 ||
		cfg.Contribution != "4,8gb" || cfg.LocalReserve != "1,2gb" ||
		cfg.LocalAdmission == nil || !*cfg.LocalAdmission || len(cfg.Labels) != 1 {
		t.Fatalf("claim-mode config = %+v", cfg)
	}
}

func writePrivateConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// The agent asks the controller for a per-run cache grant, so a config that
// still hands it the operator cache token is refused, naming the key to delete.
func TestConfig_RefusesACacheToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("controller: http://localhost:4344\ncache_token: operator-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cache_token") {
		t.Fatalf("Load error = %v, want one naming cache_token", err)
	}
}

// An allow list without a gitcache is the direct mode: the agent fetches each
// run's source itself instead of through the controller's proxy. With no list
// the agent keeps the proxy, which is what an old agent.yaml gets.
func TestConfig_AllowReposSelectsDirectSource(t *testing.T) {
	norm, err := Validate(Config{Controller: "http://x", AllowRepos: []string{"GitHub.com/Acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	if norm.Gitcache != "" {
		t.Fatalf("gitcache = %q, want none so the agent fetches directly", norm.Gitcache)
	}
	if len(norm.AllowRepos) != 1 || norm.AllowRepos[0] != "github.com/acme/*" {
		t.Fatalf("allow_repos = %v, want the canonical pattern", norm.AllowRepos)
	}
	explicit, err := Validate(Config{Controller: "http://x", Gitcache: "http://cache", AllowRepos: []string{"github.com/acme/*"}})
	if err != nil || explicit.Gitcache != "http://cache" {
		t.Fatalf("an explicit gitcache with a list = %q, %v; want the gitcache kept", explicit.Gitcache, err)
	}
	if _, err := Validate(Config{Controller: "http://x", AllowRepos: []string{"https://github.com/acme/*"}}); err == nil ||
		!strings.Contains(err.Error(), "allow_repos") {
		t.Fatalf("a pattern with a scheme = %v, want allow_repos refused", err)
	}
}

func TestLoad_ReadsAllowRepos(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("controller: http://x\nallow_repos:\n  - github.com/acme/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || len(cfg.AllowRepos) != 1 || cfg.AllowRepos[0] != "github.com/acme/*" {
		t.Fatalf("load = %+v, %v", cfg, err)
	}
}
