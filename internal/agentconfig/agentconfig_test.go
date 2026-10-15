package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestLoadReadsPrivateRunnerCredential(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "agent-token")
	if err := fssecure.WriteFile(credential, []byte("runner-private-token\n")); err != nil {
		t.Fatal(err)
	}
	path := writePrivateConfig(t, fmt.Sprintf("controller: https://controller.example\ncredentials_dir: %q\n", dir))
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "runner-private-token" {
		t.Fatal("runner credential directory was not consumed")
	}
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("missing configured credential was silently accepted")
	}
}

func TestConfig_RoundTripFromYAML(t *testing.T) {
	dir := t.TempDir()
	if err := fssecure.WriteFile(filepath.Join(dir, "agent-token"), []byte("private-runner-token")); err != nil {
		t.Fatal(err)
	}
	path := writePrivateConfig(t, fmt.Sprintf(`controller: http://localhost:4344
credentials_dir: %q
max_concurrent: 3
labels:
  - laptop
  - arch=arm64
spawn_policy: return-to-queue
`, dir))

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Controller != "http://localhost:4344" {
		t.Fatalf("controller: %q", cfg.Controller)
	}
	if cfg.CredentialsDir != dir {
		t.Fatalf("credentials directory = %q", cfg.CredentialsDir)
	}
	if cfg.MaxConcurrent != 3 {
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
		{"unknown field", "runner:\n  controller: http://localhost:4344\n  admin: true\n", "runner.admin"},
		{"removed gitcache key", "runner:\n  controller: http://localhost:4344\n  gitcache: http://cache\n", "runner.gitcache"},
		{"removed logs key", "runner:\n  controller: http://localhost:4344\n  logs: http://localhost:4345\n", "runner.logs"},
		{"second document", "runner:\n  controller: http://localhost:4344\n---\nrunner:\n  token: hidden\n", "expected one YAML document"},
		{"no agent section", "profiles: {}\n", "has no runner section"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRawConfig(t, tc.body)
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
			if err == nil || !strings.Contains(err.Error(), "unknown key") {
				t.Fatalf("Load error = %v, want %q", err, "unknown key")
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("refusal does not name the file: %v", err)
			}
		})
	}
}

func TestLoad_KeepsEveryClaimModeSetting(t *testing.T) {
	path := writePrivateConfig(t, `controller: http://localhost:4344
holder_prefix: dev-laptop
max_concurrent: 2
contribution: 4,8gb
local_admission: true
keep_free: 1,2gb
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
	if cfg.Controller != "http://localhost:4344" ||
		cfg.HolderPrefix != "dev-laptop" || cfg.MaxConcurrent != 2 ||
		cfg.Contribution != "4,8gb" || cfg.LocalReserve != "1,2gb" ||
		cfg.LocalAdmission == nil || !*cfg.LocalAdmission || len(cfg.Labels) != 1 {
		t.Fatalf("claim-mode config = %+v", cfg)
	}
}

func writePrivateConfig(t *testing.T, body string) string {
	t.Helper()
	return writeRawConfig(t, "runner:\n  "+strings.ReplaceAll(strings.TrimSuffix(body, "\n"), "\n", "\n  ")+"\n")
}

func writeRawConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// safety: cache service credentials have no owner in agent configuration.
func TestConfig_RefusesACacheToken(t *testing.T) {
	path := writePrivateConfig(t, "controller: http://localhost:4344\ncache_token: operator-token\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cache_token") {
		t.Fatalf("Load error = %v, want one naming cache_token", err)
	}
}

func TestConfig_ValidatesAllowRepos(t *testing.T) {
	norm, err := Validate(Config{Controller: "http://x", AllowRepos: []string{"GitHub.com/Acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(norm.AllowRepos) != 1 || norm.AllowRepos[0] != "github.com/acme/*" {
		t.Fatalf("allow_repos = %v, want the canonical pattern", norm.AllowRepos)
	}
	if _, err := Validate(Config{Controller: "http://x", AllowRepos: []string{"https://github.com/acme/*"}}); err == nil ||
		!strings.Contains(err.Error(), "allow_repos") {
		t.Fatalf("a pattern with a scheme = %v, want allow_repos refused", err)
	}
}

func TestLoad_ReadsAllowRepos(t *testing.T) {
	path := writePrivateConfig(t, "controller: http://x\nrepos:\n  - github.com/acme/*\n")
	cfg, err := Load(path)
	if err != nil || len(cfg.AllowRepos) != 1 || cfg.AllowRepos[0] != "github.com/acme/*" {
		t.Fatalf("load = %+v, %v", cfg, err)
	}
}

func TestValidate_AcceptWorkingTreeNeedsAnAllowlist(t *testing.T) {
	if _, err := Validate(Config{Controller: "https://c.example", AcceptWorkingTree: true}); err == nil ||
		!strings.Contains(err.Error(), "accept_working_tree needs allow_repos") {
		t.Fatalf("Validate without allow_repos = %v, want the refusal", err)
	}
	cfg, err := Validate(Config{Controller: "https://c.example", AllowRepos: []string{"github.com/acme/*"}, AcceptWorkingTree: true})
	if err != nil || !cfg.AcceptWorkingTree {
		t.Fatalf("Validate with allow_repos = %+v, %v", cfg, err)
	}
}
