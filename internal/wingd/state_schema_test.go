package wingd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

func TestStateIsWrittenAtTheCurrentSchema(t *testing.T) {
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
		t.Fatalf("state schema = %d, want current schema %d", state.Schema, stateSchema)
	}
}

func TestStateRejectsRemovedSchemas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"schema":2,"snapshot":{"event_seq":4}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readStateWithCancellations(path); err == nil {
		t.Fatal("removed schema was accepted")
	}
}
