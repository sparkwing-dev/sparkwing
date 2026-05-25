package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func boolPtr(b bool) *bool { return &b }

func TestBuildRunInvocation_NoProfileOmitsBlocks(t *testing.T) {
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1")
	if _, ok := inv["profile"]; ok {
		t.Errorf("profile block must be omitted when opts.Profile is nil; got %v", inv["profile"])
	}
	if _, ok := inv["backends"]; ok {
		t.Errorf("backends block must be omitted when opts.Profile is nil; got %v", inv["backends"])
	}
}

func TestBuildRunInvocation_ProfileSetButNoChainOmits(t *testing.T) {
	// Defensive: a profile without a chain (caller forgot, legacy path)
	// emits nothing rather than a partial block.
	opts := Options{Pipeline: "demo", Profile: &profile.Profile{Name: "prod", Controller: "https://api.example.dev"}}
	inv := buildRunInvocation(opts, "run-1")
	if _, ok := inv["profile"]; ok {
		t.Error("profile block must be omitted when ProfileChain is nil")
	}
}

func TestBuildRunInvocation_FlagSourceController(t *testing.T) {
	opts := Options{
		Pipeline:     "demo",
		Profile:      &profile.Profile{Name: "prod", Controller: "https://api.example.dev", Token: "swu_secret"},
		ProfileChain: &profile.Chain{Selected: "prod", Source: profile.ChainSourceFlag},
	}
	inv := buildRunInvocation(opts, "run-1")
	prof, ok := inv["profile"].(map[string]any)
	if !ok {
		t.Fatalf("profile block missing or wrong type: %#v", inv["profile"])
	}
	if prof["name"] != "prod" || prof["source"] != "flag" || prof["detect_via"] != "" || prof["mirror_local"] != true {
		t.Errorf("profile block = %#v", prof)
	}
	be, ok := inv["backends"].(map[string]any)
	if !ok {
		t.Fatalf("backends block missing: %#v", inv["backends"])
	}
	if be["state"] != "controller://prod" || be["logs"] != "controller://prod" || be["cache"] != "controller://prod" {
		t.Errorf("backends block = %#v", be)
	}
	// Neither the controller URL nor the token may appear anywhere.
	if _, leaked := prof["controller"]; leaked {
		t.Error("profile block must not carry a controller field")
	}
	for k, v := range prof {
		if s, ok := v.(string); ok && (s == "https://api.example.dev" || s == "swu_secret") {
			t.Errorf("leaked controller/token via %s=%q", k, s)
		}
	}
}

func TestBuildRunInvocation_DetectSource(t *testing.T) {
	opts := Options{
		Pipeline: "demo",
		Profile:  &profile.Profile{Name: "gha", State: &backends.Spec{Type: backends.TypeS3, Bucket: "ci", Prefix: "state"}},
		ProfileChain: &profile.Chain{
			Selected: "gha", Source: profile.ChainSourceDetect, DetectVia: "GITHUB_ACTIONS",
		},
	}
	inv := buildRunInvocation(opts, "run-1")
	prof := inv["profile"].(map[string]any)
	if prof["source"] != "detect" {
		t.Errorf("source = %v, want detect", prof["source"])
	}
	if prof["detect_via"] != "GITHUB_ACTIONS" {
		t.Errorf("detect_via = %v, want GITHUB_ACTIONS", prof["detect_via"])
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
	inv := buildRunInvocation(opts, "run-1")
	prof := inv["profile"].(map[string]any)
	if prof["mirror_local"] != false {
		t.Errorf("mirror_local = %v, want false", prof["mirror_local"])
	}
}

func TestBuildRunInvocation_S3StateNoController(t *testing.T) {
	opts := Options{
		Pipeline:     "demo",
		Profile:      &profile.Profile{Name: "team", State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"}},
		ProfileChain: &profile.Chain{Selected: "team", Source: profile.ChainSourceDefault},
	}
	inv := buildRunInvocation(opts, "run-1")
	be := inv["backends"].(map[string]any)
	if be["state"] != "s3://team/state" {
		t.Errorf("state = %v, want s3://team/state", be["state"])
	}
	prof := inv["profile"].(map[string]any)
	if _, ok := prof["controller"]; ok {
		t.Error("controller-less profile must not emit a controller field")
	}
}
