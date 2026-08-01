package main

import (
	"encoding/json"
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
