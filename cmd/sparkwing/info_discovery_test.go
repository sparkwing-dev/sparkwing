package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInfoDiscoveryIsEphemeralAndVersioned(t *testing.T) {
	if !strings.Contains(agentBlockHeader, "must not be persisted") {
		t.Fatalf("agent discovery header does not bound what may be kept: %q", agentBlockHeader)
	}
	raw, err := json.Marshal(Info{CapabilityEpoch: infoCapabilityEpoch})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"capability_epoch":1`) {
		t.Fatalf("structured info has no capability epoch: %s", raw)
	}
}

func TestDurableAgentBlockCarriesNothingThatRots(t *testing.T) {
	if !strings.Contains(agentBlockDurable, "sparkwing info --for-agent") {
		t.Error("durable block must name the command that reports everything current")
	}
	banned := map[string]string{
		"capability epoch": "epoch",
		"a version string": "v0.",
		"an absolute path": "/Users",
		"a pipeline count": "pipeline(s)",
	}
	for what, needle := range banned {
		if strings.Contains(agentBlockDurable, needle) {
			t.Errorf("durable block carries %s (%q); it will be stale in a pasted file", what, needle)
		}
	}
	if n := strings.Count(agentBlockDurable, "sparkwing "); n > 1 {
		t.Errorf("durable block names %d commands; one entry point means a stale copy can misdirect in exactly one way", n)
	}
	if lines := strings.Count(strings.TrimSpace(agentBlockDurable), "\n") + 1; lines > 5 {
		t.Errorf("durable block is %d lines; it is pasted into every repo that adopts sparkwing", lines)
	}
}

func TestAuthoringQuickstartIsNotDurable(t *testing.T) {
	if strings.Contains(agentBlockDurable, "pipeline new") ||
		strings.Contains(agentBlockDurable, "--guide") {
		t.Error("authoring commands leaked into the durable block")
	}
	for _, want := range []string{"--template <shape>", "--guide authoring", "pipeline lint"} {
		if !strings.Contains(agentBlockAuthoring, want) {
			t.Errorf("authoring quickstart does not mention %q", want)
		}
	}
}

func TestRepositoryAgentGuidanceIsStandalone(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate agent guidance test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	guidance := strings.ToLower(string(agents))
	for _, privateTool := range []string{"bitwing", "flockwing", "overwing", "xwing"} {
		if strings.Contains(guidance, privateTool) {
			t.Errorf("AGENTS.md assumes private tool %q", privateTool)
		}
	}
	if !strings.Contains(guidance, "sparkwing info --for-agent") {
		t.Error("AGENTS.md does not route agents through Sparkwing discovery")
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(claude) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q, want the exact AGENTS.md pointer", claude)
	}
}
