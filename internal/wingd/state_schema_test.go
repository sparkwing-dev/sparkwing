package wingd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

func TestStateIsWrittenAtTheRollbackReadableSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeStateWithCancellations(path, admission.Snapshot{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(blob, &state); err != nil {
		t.Fatal(err)
	}
	if state.Schema != stateSchema {
		t.Fatalf("state schema = %d, want rollback-readable schema %d", state.Schema, stateSchema)
	}
}

func TestStateWrittenByAGuardedDaemonStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := map[string]any{
		"schema":   legacyGuardedStateSchema,
		"snapshot": admission.Snapshot{EventSeq: 4},
		"guards": []map[string]any{{
			"lease_id": "lease-1",
			"run_id":   "run-1",
			"session":  map[string]any{"leader_pid": 37, "session_id": 37, "birth_token": "birth-37"},
		}},
	}
	blob, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	snap, _, _, err := readStateWithCancellations(path)
	if err != nil {
		t.Fatalf("read a state file left by a guarded daemon: %v", err)
	}
	if snap.EventSeq != 4 {
		t.Fatalf("restored event sequence = %d, want 4", snap.EventSeq)
	}
}
