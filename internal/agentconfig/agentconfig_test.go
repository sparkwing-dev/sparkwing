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
cache_token: cache-abc
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
	if cfg.Gitcache != "http://localhost:4344/api/v1/gitcache" || cfg.CacheToken != "cache-abc" {
		t.Fatalf("gitcache credentials: url=%q token=%q", cfg.Gitcache, cfg.CacheToken)
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
