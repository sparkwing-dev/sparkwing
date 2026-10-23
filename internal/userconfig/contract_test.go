package userconfig

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestFinalMachineSchema(t *testing.T) {
	cfg, err := Decode("machine.yaml", []byte(`profiles: {prod: {controller: {url: https://example.com, token: secret}}}
runner: {repos: [github.com/acme/repo], accept_working_tree: true, max_concurrent: 1, lease: 1m, heartbeat: 10s, spawn_policy: return-to-queue}
cache: {max_bytes: "0", max_entries: 0}
admission: {mode: custom, custom: {aging_every: 0, class_weight: {batch: 0}}}
machine: {box_id: machine-one}
run: {submit_env_allow: []}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cache.MaxEntries == nil || *cfg.Cache.MaxEntries != 0 || cfg.Run.SubmitEnvAllow == nil || cfg.Run.CacheFrom != nil || cfg.Machine.BoxID == nil {
		t.Fatal("presence lost")
	}
}

func TestRejectMalformedMachine(t *testing.T) {
	for _, raw := range []string{"machine: {box_id: ''}", "agent: {}", "logs: {}", "runner: {gitcache: x}", "runner: {allow_repos: []}", "run: {dispatch_wait: 1m}", "profiles: {p: {backend: x}}", "cache: null", "cache: {max_entries: 1, max_entries: 2}", "{}\n---\n{}", "runner: {max_concurrent: 0}", "runner: {lease: 0s}", "runner: {heartbeat: 0s}", "runner: {accept_working_tree: true}", "debug: {pause_timeout: 0s}", "admission: {mode: wrong}"} {
		t.Run(raw, func(t *testing.T) {
			_, err := Decode("machine.yaml", []byte(raw))
			if err == nil {
				t.Fatal("accepted invalid YAML")
			}
			if !strings.Contains(err.Error(), "machine.yaml") || !strings.Contains(err.Error(), "line") {
				t.Fatalf("missing location: %v", err)
			}
		})
	}
}

func TestRunnerLabelsReserveCloudPrefix(t *testing.T) {
	for _, label := range []string{"sparkwing-16cpu", " sparkwing-future "} {
		_, err := Decode("machine.yaml", []byte("runner: {labels: ["+label+"]}"))
		if err == nil || !strings.Contains(err.Error(), "runner.labels") || !strings.Contains(err.Error(), "belong to Sparkwing Cloud") {
			t.Fatalf("reserved label %q: %v", label, err)
		}
	}
	if _, err := Decode("machine.yaml", []byte("runner: {labels: [team-16cpu, gpu]}")); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsDoNotEchoTokens(t *testing.T) {
	for _, raw := range []string{"profiles: {p: {controller: {url: [secret-token], token: secret-token}}}", "profiles: {p: {controller: {url: secret-token, token: secret-token}}}", "profiles: {p: {controller: {token: {secret-token: x}}}}"} {
		_, err := Decode("machine.yaml", []byte(raw))
		if err == nil {
			t.Fatal("accepted invalid config")
		}
		if strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("secret leaked: %v", err)
		}
	}
}

func TestReadConfigPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := fssecure.WriteFile(path, []byte("run: {submit_env_allow: []}")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Skip(err)
	}
	if _, err := ReadConfig(link); err == nil {
		t.Fatal("followed symlink")
	}
}

func TestSemanticErrorsRetainLocations(t *testing.T) {
	_, err := Decode("machine.yaml", []byte("profiles:\n  company.prod:\n    controller:\n      url: bad\n"))
	if err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("wrong location: %v", err)
	}
	_, err = Decode("machine.yaml", []byte("runner:\n  lease: &lease 1m\n  heartbeat: *lease\n"))
	if err == nil || !strings.Contains(err.Error(), "heartbeat") {
		t.Fatalf("alias escaped semantic validation: %v", err)
	}
}

func TestSemanticErrorsLocatePresentMergeSource(t *testing.T) {
	_, err := Decode("machine.yaml", []byte("runner:\n  <<:\n    - {}\n    - max_concurrent: 0\n"))
	if err == nil || !strings.Contains(err.Error(), "runner.max_concurrent") || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("merge error must name the field's defining source: %v", err)
	}
}

func TestConfigAliasesAndMergesRemainStrict(t *testing.T) {
	raw := []byte("profiles:\n  prod: &base\n    controller: {url: https://example.com}\n  lab: {<<: *base}\n")
	cfg, err := Decode("machine.yaml", raw)
	if err != nil || cfg.Profiles["lab"].Controller.URL != "https://example.com" {
		t.Fatalf("merge: %+v %v", cfg, err)
	}
	for _, raw := range []string{"runner: &loop {repos: [*loop]}", "runner: {<<: {gitcache: wrong}}", "profiles: {p: {controller: {url: [secret-token}}}"} {
		_, err := Decode("machine.yaml", []byte(raw))
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("bad refusal: %v", err)
		}
	}
}

func TestAliasValidationUsesEachDestinationSchema(t *testing.T) {
	_, err := Decode("machine.yaml", []byte("profiles: {p: {controller: &connection {url: https://example.com}}}\nrunner: *connection\n"))
	if err == nil || !strings.Contains(err.Error(), "runner.url") || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("alias bypassed runner schema: %v", err)
	}
}

func TestTypedMachineValidationMatchesDecode(t *testing.T) {
	zero := 0
	cfg := Config{Runner: &RunnerConfig{MaxConcurrent: &zero}}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("typed validation accepted zero")
	}
	for _, raw := range []string{"NaNGB", "+InfGB", "1e99GB", "-1", ""} {
		if _, err := ParseByteSize(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"NaN", "Inf", "0%", "101%", "-1gb", "1e99gb"} {
		if _, err := ParseBudget(raw); err == nil {
			t.Errorf("accepted budget %q", raw)
		}
	}
}

func FuzzRetiredProfileFieldsKeepValuesPrivate(f *testing.F) {
	f.Add([]byte("private-profile-value"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) == 0 || len(input) > 256 {
			return
		}
		value := "private-" + hex.EncodeToString(input)
		for _, key := range []string{"state", "secrets", "logs", "cache", "mirror_local"} {
			_, err := Decode("machine.yaml", []byte("profiles: {company.prod: {"+key+": {token: "+value+"}}}"))
			var field *FieldError
			if !errors.As(err, &field) || field.Key != "profiles.company.prod."+key || field.Line != 1 || !strings.Contains(field.Problem, "is removed") || !strings.Contains(field.Problem, "https://sparkwing.dev/docs/migration-guide/") || strings.Contains(err.Error(), value) {
				t.Fatalf("removal diagnostic lost its location, guidance or privacy: %v", err)
			}
		}
	})
}

func TestRejectRemovedAdmissionAdvisor(t *testing.T) {
	for _, raw := range []string{"admission: {mode: jev}", "admission: {jev: {}}"} {
		if _, err := Decode("machine.yaml", []byte(raw)); err == nil {
			t.Fatalf("accepted removed admission policy: %s", raw)
		}
	}
}
