package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

func TestRunPublishesHandleAfterPersistence(t *testing.T) {
	paths := newPaths(t)
	handlePath := filepath.Join(t.TempDir(), "run.json")
	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:      "orch-ok",
		RunHandlePath: handlePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(handlePath)
	if err != nil {
		t.Fatal(err)
	}
	var handle struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		Pipeline      string `json:"pipeline"`
	}
	if err := json.Unmarshal(body, &handle); err != nil {
		t.Fatal(err)
	}
	if handle.SchemaVersion != 1 || handle.RunID != res.RunID || handle.Pipeline != "orch-ok" {
		t.Fatalf("handle = %#v; result = %#v", handle, res)
	}
}

func TestRunRefusesWorkWhenHandleCannotBePublished(t *testing.T) {
	paths := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:      "orch-ok",
		RunHandlePath: filepath.Join(t.TempDir(), "missing", "run.json"),
	})
	if err == nil {
		t.Fatal("run succeeded without publishing its requested handle")
	}
}
