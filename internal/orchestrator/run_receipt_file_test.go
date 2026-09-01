package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

func TestRunPublishesReceiptAfterPersistence(t *testing.T) {
	paths := newPaths(t)
	receiptPath := filepath.Join(t.TempDir(), "run.json")
	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:       "orch-ok",
		RunReceiptPath: receiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		Pipeline      string `json:"pipeline"`
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.SchemaVersion != 1 || receipt.RunID != res.RunID || receipt.Pipeline != "orch-ok" {
		t.Fatalf("receipt = %#v; result = %#v", receipt, res)
	}
}

func TestRunRefusesWorkWhenReceiptCannotBePublished(t *testing.T) {
	paths := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:       "orch-ok",
		RunReceiptPath: filepath.Join(t.TempDir(), "missing", "run.json"),
	})
	if err == nil {
		t.Fatal("run succeeded without publishing its requested receipt")
	}
}
