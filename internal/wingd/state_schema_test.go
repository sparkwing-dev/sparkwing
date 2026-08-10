package wingd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestStateSchemaRemainsRollbackReadableWithoutGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeStateWithGuards(path, admission.Snapshot{}, nil, nil, nil); err != nil {
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
	if state.Schema != 1 {
		t.Fatalf("unguarded state schema = %d, want rollback-readable schema 1", state.Schema)
	}

	guard := persistedGuard{
		LeaseID: "lease-1",
		RunID:   "run-1",
		Session: wingwire.ProcessSession{LeaderPID: 37, SessionID: 37, BirthToken: "birth-37"},
	}
	if err := writeStateWithGuards(path, admission.Snapshot{}, nil, nil, []persistedGuard{guard}); err != nil {
		t.Fatal(err)
	}
	blob, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blob, &state); err != nil {
		t.Fatal(err)
	}
	if state.Schema != stateSchema {
		t.Fatalf("guarded state schema = %d, want %d", state.Schema, stateSchema)
	}
}
