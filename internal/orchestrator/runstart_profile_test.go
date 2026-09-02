package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func boolPtr(b bool) *bool { return &b }

func TestBuildRunInvocation_NoProfileOmitsBlocks(t *testing.T) {
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", "", nil)
	if _, ok := inv["profile"]; ok {
		t.Errorf("profile block must be omitted when opts.Profile is nil; got %v", inv["profile"])
	}
	if _, ok := inv["backends"]; ok {
		t.Errorf("backends block must be omitted when opts.Profile is nil; got %v", inv["backends"])
	}
}

func TestBuildRunInvocation_ProfileSetButNoChainOmits(t *testing.T) {
	opts := Options{Pipeline: "demo", Profile: &profile.Profile{Name: "prod", Controller: &profile.ControllerSpec{URL: "https://api.example.dev"}}}
	inv := buildRunInvocation(opts, "run-1", "", nil)
	if _, ok := inv["profile"]; ok {
		t.Error("profile block must be omitted when ProfileChain is nil")
	}
}

func TestBuildRunInvocation_LocalOnlyReportsEffectiveBackends(t *testing.T) {
	t.Setenv("SPARKWING_ALLOW", "")
	t.Setenv("SPARKWING_PROFILE", "dead-profile")
	t.Setenv("SPARKWING_SECRETS_PROFILE", "dead-secrets")
	opts := Options{
		Pipeline:     "demo",
		LocalOnly:    true,
		Profile:      &profile.Profile{Name: "prod", Controller: &profile.ControllerSpec{URL: "https://api.example.dev"}},
		ProfileChain: &profile.Chain{Selected: "prod", Source: profile.ChainSourceFlag},
	}
	inv := buildRunInvocation(opts, "run-1", "", nil)
	if _, ok := inv["profile"]; ok {
		t.Fatalf("local-only invocation reported an active profile: %#v", inv["profile"])
	}
	be, ok := inv["backends"].(map[string]any)
	if !ok || be["state"] != "sqlite" || be["logs"] != "filesystem" || be["cache"] != "filesystem" {
		t.Fatalf("local-only backends = %#v", inv["backends"])
	}
	flags, ok := inv["flags"].(map[string]any)
	if !ok || flags["local_only"] != true {
		t.Fatalf("local-only flags = %#v", inv["flags"])
	}
	if _, ok := flags["secrets"]; ok {
		t.Fatalf("local-only flags reported inactive secrets profile: %#v", flags)
	}
	if _, ok := flags["profile"]; ok {
		t.Fatalf("local-only flags reported inactive profile: %#v", flags)
	}
	if inv["reproducer"] != "sparkwing run demo --sw-local-only" {
		t.Fatalf("local-only reproducer = %q", inv["reproducer"])
	}
}

func TestBuildRunInvocation_FlagSourceController(t *testing.T) {
	t.Setenv("SPARKWING_PROFILE", "prod")
	t.Setenv("SPARKWING_SECRETS_PROFILE", "vault")
	opts := Options{
		Pipeline:     "demo",
		Profile:      &profile.Profile{Name: "prod", Controller: &profile.ControllerSpec{URL: "https://api.example.dev", Token: "swu_secret"}},
		ProfileChain: &profile.Chain{Selected: "prod", Source: profile.ChainSourceFlag},
	}
	inv := buildRunInvocation(opts, "run-1", "", nil)
	prof, ok := inv["profile"].(map[string]any)
	if !ok {
		t.Fatalf("profile block missing or wrong type: %#v", inv["profile"])
	}
	if prof["name"] != "prod" || prof["source"] != "flag" || prof["mirror_local"] != true {
		t.Errorf("profile block = %#v", prof)
	}
	be, ok := inv["backends"].(map[string]any)
	if !ok {
		t.Fatalf("backends block missing: %#v", inv["backends"])
	}
	if be["state"] != "controller://prod" || be["logs"] != "controller://prod" || be["cache"] != "controller://prod" {
		t.Errorf("backends block = %#v", be)
	}
	if _, leaked := prof["controller"]; leaked {
		t.Error("profile block must not carry a controller field")
	}
	flags := inv["flags"].(map[string]any)
	if flags["profile"] != "prod" || flags["secrets"] != "vault" {
		t.Errorf("normal profile flags = %#v", flags)
	}
	for k, v := range prof {
		if s, ok := v.(string); ok && (s == "https://api.example.dev" || s == "swu_secret") {
			t.Errorf("leaked controller/token via %s=%q", k, s)
		}
	}
}

func TestBuildRunInvocation_MirrorLocalFalse(t *testing.T) {
	opts := Options{
		Pipeline: "demo",
		Profile: &profile.Profile{
			Name:        "ci",
			State:       &backends.Spec{Type: backends.TypeS3, Bucket: "ci", Prefix: "state"},
			MirrorLocal: boolPtr(false),
		},
		ProfileChain: &profile.Chain{Selected: "ci", Source: profile.ChainSourceFlag},
	}
	inv := buildRunInvocation(opts, "run-1", "", nil)
	prof := inv["profile"].(map[string]any)
	if prof["mirror_local"] != false {
		t.Errorf("mirror_local = %v, want false", prof["mirror_local"])
	}
}

func TestBuildRunInvocation_S3StateNoController(t *testing.T) {
	opts := Options{
		Pipeline:     "demo",
		Profile:      &profile.Profile{Name: "team", State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"}},
		ProfileChain: &profile.Chain{Selected: "team", Source: profile.ChainSourceFlag},
	}
	inv := buildRunInvocation(opts, "run-1", "", nil)
	be := inv["backends"].(map[string]any)
	if be["state"] != "s3://team/state" {
		t.Errorf("state = %v, want s3://team/state", be["state"])
	}
	prof := inv["profile"].(map[string]any)
	if _, ok := prof["controller"]; ok {
		t.Error("controller-less profile must not emit a controller field")
	}
}
