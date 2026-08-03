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
	if !strings.Contains(agentBlockHeader, "Use for this agent wake only") {
		t.Fatalf("agent discovery header has no one-wake lifetime: %q", agentBlockHeader)
	}
	if strings.Contains(strings.ToLower(agentBlockHeader), "paste") || strings.Contains(agentBlockHeader, "CLAUDE.md") || strings.Contains(agentBlockHeader, "AGENTS.md") {
		t.Fatalf("agent discovery contains durable-copy guidance: %q", agentBlockHeader)
	}
	raw, err := json.Marshal(Info{CapabilityEpoch: infoCapabilityEpoch})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"capability_epoch":1`) {
		t.Fatalf("structured info has no capability epoch: %s", raw)
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
