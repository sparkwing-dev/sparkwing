package fleet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPolicyProjectionReportsOnlyConfiguredTrustAndCapacity(t *testing.T) {
	cfg := Config{
		Listen: "127.0.0.1:4346", PublicURL: "https://private.example.test",
		Local: Local{
			Name: "laptop", Capabilities: []string{"toolchain=go"},
			MaxConcurrent: 1, Contribution: "50%,50%", LocalReserve: "2,4gb",
		},
		Executors: []Executor{{
			Name: "desk", Location: "local", Capabilities: []string{"toolchain=go"},
			BasePriority: 50, PriorityCeiling: 80, MaxConcurrent: 2,
			Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 8 << 30},
		}},
	}
	payload, err := json.Marshal(cfg.PolicyProjection())
	if err != nil {
		t.Fatal(err)
	}
	got := string(payload)
	for _, want := range []string{
		`"name":"laptop"`, `"placement":"coordinator"`, `"contribution":"50%,50%"`,
		`"trusted_capabilities":["toolchain=go"]`, `"name":"desk"`, `"kind":"agent"`,
		`"placement":"local"`, `"base_priority":50`,
		`"max_concurrent":2`, `"memory_bytes":8589934592`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("policy projection %s lacks %s", got, want)
		}
	}
	for _, forbidden := range []string{
		"private.example.test", "127.0.0.1", "token", "prefix", "credential", "principal",
		"eligible", "online", "offer", "award", "reservation", "holder",
	} {
		if strings.Contains(strings.ToLower(got), forbidden) {
			t.Errorf("policy projection %s exposes or invents %q", got, forbidden)
		}
	}
}
