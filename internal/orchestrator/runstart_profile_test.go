package orchestrator

import (
	"testing"
)

func TestBuildRunInvocation_NoProfileOmitsBlocks(t *testing.T) {
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", "", nil)
	if _, ok := inv["profile"]; ok {
		t.Errorf("profile block must be omitted from a run that is not local-only; got %v", inv["profile"])
	}
	if _, ok := inv["backends"]; ok {
		t.Errorf("backends block must be omitted from a run that is not local-only; got %v", inv["backends"])
	}
}

func TestBuildRunInvocation_LocalOnlyReportsEffectiveBackends(t *testing.T) {
	t.Setenv("SPARKWING_ALLOW", "")
	// safety: the reproducer names every flag the environment implies, so a
	// variable this test does not clear becomes an argument it did not expect.
	t.Setenv("SPARKWING_NO_UPDATE", "")
	t.Setenv("SPARKWING_LOG_LEVEL", "")
	t.Setenv("SPARKWING_PROFILE", "dead-profile")
	t.Setenv("SPARKWING_SECRETS_PROFILE", "dead-secrets")
	opts := Options{
		Pipeline:  "demo",
		LocalOnly: true,
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
