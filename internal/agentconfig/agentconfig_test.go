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

func TestConfig_RequiresLocalAdmissionOnlyForEnrolledMode(t *testing.T) {
	disabled := false
	legacy, err := Validate(Config{Controller: "http://x", LocalAdmission: &disabled})
	if err != nil || legacy.LocalAdmission == nil || *legacy.LocalAdmission {
		t.Fatalf("legacy local_admission:false = %+v, %v", legacy.LocalAdmission, err)
	}
	if _, err := Validate(Config{Name: "desk", Controller: "http://x", Token: "swr_x", LocalAdmission: &disabled}); err == nil {
		t.Fatal("enrolled local_admission:false was accepted")
	}
	enrolled, err := Validate(Config{Name: "desk", Controller: "http://x", Token: "swr_x"})
	if err != nil || enrolled.LocalAdmission == nil || !*enrolled.LocalAdmission {
		t.Fatalf("enrolled local admission default = %+v, %v", enrolled.LocalAdmission, err)
	}
}

func TestConfig_MultipleMembershipsRequireDistinctCredentialsAndShareCeilings(t *testing.T) {
	cfg := Config{
		Name: "desk", MaxConcurrent: 3, Contribution: "4,8gb",
		Coordinators: []Coordinator{
			{Controller: "https://personal.example", Token: "swr_personal", MaxConcurrent: 2},
			{Controller: "https://team.example", Token: "swr_team", MaxConcurrent: 9, Contribution: "2,4gb"},
		},
	}
	norm, err := Validate(cfg)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if norm.Coordinators[0].Name != "desk" || norm.Coordinators[1].Name != "desk" ||
		norm.Coordinators[0].Contribution != "4,8gb" || norm.Coordinators[1].MaxConcurrent != 3 {
		t.Fatalf("membership ceilings = %+v", norm.Coordinators)
	}
	cfg.Coordinators[1].Token = "swr_personal"
	if _, err := Validate(cfg); err == nil {
		t.Fatal("duplicate membership credential was accepted")
	}
}

func TestCheckEnrolledExecutionAvailable_RefusesEveryEnrolledShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"named singular", Config{Controller: "http://x", Name: "desk", Token: "tok"}},
		{"coordinators", Config{Controller: "http://x", Coordinators: []Coordinator{
			{Name: "desk", Controller: "http://x", Token: "tok"},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Validate(tc.cfg)
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			err = CheckEnrolledExecutionAvailable(cfg, false)
			if err == nil || err.Error() != EnrolledExecutionUnavailable {
				t.Fatalf("refusal = %v, want %q", err, EnrolledExecutionUnavailable)
			}
			if err := CheckEnrolledExecutionAvailable(cfg, true); err != nil {
				t.Fatalf("preview override: %v", err)
			}
		})
	}
}

func TestCheckEnrolledExecutionAvailable_AdmitsClaimMode(t *testing.T) {
	cfg, err := Validate(Config{Controller: "http://x", Token: "tok", HolderPrefix: "desk"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := CheckEnrolledExecutionAvailable(cfg, false); err != nil {
		t.Fatalf("claim mode refused: %v", err)
	}
}
