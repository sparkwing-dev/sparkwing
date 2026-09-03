package local

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func lastValue(env []string, key string) (string, bool) {
	out, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			out, found = v, true
		}
	}
	return out, found
}

func testConfig() Config {
	return Config{
		Executable:    "/opt/pipeline",
		ControllerURL: "http://127.0.0.1:52341",
		AgentToken:    "svc_tok",
		WorkDir:       "/repo",
		Home:          "/home/dev/.sparkwing",
		CacheURL:      "fs:///home/dev/.sparkwing/cache",
		Labels:        []string{"local", "os=darwin"},
		DryRun:        true,
		StartAt:       "build",
		StopAt:        "deploy",
		LeaseTokens: func(context.Context) (string, string) {
			return "lease-abc", "child-def"
		},
	}
}

func TestChildEnv_PinsTheNodeContract(t *testing.T) {
	env := childEnv(context.Background(), nil, testConfig(),
		runner.Request{RunID: "run-1", NodeID: "build"})

	want := map[string]string{
		"SPARKWING_HOME":            "/home/dev/.sparkwing",
		"SPARKWING_CONTROLLER_URL":  "http://127.0.0.1:52341",
		"SPARKWING_CACHE_URL":       "fs:///home/dev/.sparkwing/cache",
		"SPARKWING_AGENT_TOKEN":     "svc_tok",
		"SPARKWING_RUN_ID":          "run-1",
		"SPARKWING_NODE_ID":         "build",
		"SPARKWING_LOG_FORMAT":      "json",
		"SPARKWING_RUNNER_NAME":     "local",
		"SPARKWING_RUNNER_TYPE":     "local",
		"SPARKWING_RUNNER_LABELS":   "local,os=darwin",
		"SPARKWING_DRY_RUN":         "1",
		"SPARKWING_START_AT":        "build",
		"SPARKWING_STOP_AT":         "deploy",
		wingwire.LeaseTokenEnv:      "lease-abc",
		wingwire.ChildLeaseTokenEnv: "child-def",
	}
	for k, v := range want {
		got, ok := lastValue(env, k)
		if !ok {
			t.Errorf("%s missing from child env", k)
			continue
		}
		if got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestChildEnv_InheritsBaseAndOverridesPinnedKeys(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"AWS_PROFILE=work",
		"SPARKWING_CONTROLLER_URL=http://stale.example",
		"SPARKWING_LOG_FORMAT=pretty",
	}
	env := childEnv(context.Background(), base, testConfig(),
		runner.Request{RunID: "run-1", NodeID: "build"})

	if v, _ := lastValue(env, "PATH"); v != "/usr/bin" {
		t.Errorf("PATH = %q, want the inherited value", v)
	}
	if v, _ := lastValue(env, "AWS_PROFILE"); v != "work" {
		t.Errorf("AWS_PROFILE = %q, want the inherited value", v)
	}
	if v, _ := lastValue(env, "SPARKWING_CONTROLLER_URL"); v != "http://127.0.0.1:52341" {
		t.Errorf("SPARKWING_CONTROLLER_URL = %q, want this run's controller", v)
	}

	if v, _ := lastValue(env, "SPARKWING_LOG_FORMAT"); v != "json" {
		t.Errorf("SPARKWING_LOG_FORMAT = %q, want json", v)
	}
}

func TestChildEnv_DoesNotSetLogsURL(t *testing.T) {
	env := childEnv(context.Background(), nil, testConfig(),
		runner.Request{RunID: "run-1", NodeID: "build"})
	if v, ok := lastValue(env, "SPARKWING_LOGS_URL"); ok {
		t.Errorf("SPARKWING_LOGS_URL = %q, want it unset", v)
	}
}

func TestChildEnv_OmitsEmptyAndUnavailableValues(t *testing.T) {
	cfg := Config{ControllerURL: "http://x", Labels: []string{"local"}}
	env := childEnv(context.Background(), nil, cfg,
		runner.Request{RunID: "run-1", NodeID: "build"})

	for _, k := range []string{
		"SPARKWING_CACHE_URL", "SPARKWING_AGENT_TOKEN", "SPARKWING_DRY_RUN",
		"SPARKWING_START_AT", "SPARKWING_STOP_AT",
		wingwire.LeaseTokenEnv, wingwire.ChildLeaseTokenEnv,
	} {
		if v, ok := lastValue(env, k); ok {
			t.Errorf("%s = %q, want it absent when unconfigured", k, v)
		}
	}
}

func TestChildEnv_APISocketCarriesNoBearer(t *testing.T) {
	cfg := testConfig()
	cfg.APISocket = "/tmp/sparkwing-501-abc/api.sock"
	base := []string{"SPARKWING_AGENT_TOKEN=inherited", "SPARKWING_TOKEN=inherited", "PATH=/usr/bin"}

	env := childEnv(context.Background(), base, cfg,
		runner.Request{RunID: "run-1", NodeID: "build"})

	if got, ok := lastValue(env, APISocketEnv); !ok || got != cfg.APISocket {
		t.Fatalf("%s = %q (found %v), want %q", APISocketEnv, got, ok, cfg.APISocket)
	}
	for _, name := range tokenEnvNames {
		if got, ok := lastValue(env, name); ok {
			t.Fatalf("%s = %q, want it stripped on the API socket path", name, got)
		}
	}
	if got, ok := lastValue(env, "PATH"); !ok || got != "/usr/bin" {
		t.Fatalf("PATH = %q (found %v), want the inherited value", got, ok)
	}
}

func TestChildEnv_LoopbackKeepsItsBearer(t *testing.T) {
	env := childEnv(context.Background(), []string{"SPARKWING_AGENT_TOKEN=inherited"}, testConfig(),
		runner.Request{RunID: "run-1", NodeID: "build"})

	if got, ok := lastValue(env, "SPARKWING_AGENT_TOKEN"); !ok || got != "svc_tok" {
		t.Fatalf("SPARKWING_AGENT_TOKEN = %q (found %v), want the loopback token", got, ok)
	}
	if got, ok := lastValue(env, APISocketEnv); ok {
		t.Fatalf("%s = %q, want it unset without an API socket", APISocketEnv, got)
	}
}

func TestChildEnv_DropsAnInheritedAPISocket(t *testing.T) {
	base := []string{APISocketEnv + "=/tmp/another-run/api.sock"}

	env := childEnv(context.Background(), base, testConfig(),
		runner.Request{RunID: "run-1", NodeID: "build"})

	if got, ok := lastValue(env, APISocketEnv); ok {
		t.Fatalf("%s = %q, want the inherited socket dropped", APISocketEnv, got)
	}
}
