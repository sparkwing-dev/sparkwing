package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func commitOutput(ctx context.Context, t *testing.T, s *store.Store, runID, nodeID string, data []byte) store.OutputRef {
	t.Helper()
	key, err := s.NodeOutputKey(ctx, runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	sha := digestOf(data)
	ref := store.OutputRef{Key: key, Size: int64(len(data)), SHA256: sha}
	if err := s.RecordLocalOutput(ctx, runID, nodeID, ref, time.Now()); err != nil {
		t.Fatalf("record output fixture: %v", err)
	}
	return ref
}

func TestFinishNodeWithOutputRef_RefusesAnotherRunsObject(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	for _, run := range []string{"run-fin-a", "run-fin-b"} {
		if err := s.CreateRun(ctx, store.Run{ID: run, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNode(ctx, store.Node{RunID: run, NodeID: "n", Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	foreign := commitOutput(t.Context(), t, s, "run-fin-b", "n", []byte(`"b"`))
	if err := s.FinishNodeWithOutputRef(ctx, "run-fin-a", "n", "success", "", &foreign, "", nil); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("finish naming another run's output: err = %v, want refused", err)
	}
	own := commitOutput(t.Context(), t, s, "run-fin-a", "n", []byte(`"a"`))
	if err := s.FinishNodeWithOutputRef(ctx, "run-fin-a", "n", "success", "", &own, "", nil); err != nil {
		t.Fatalf("finish naming its own output: %v", err)
	}
}

func TestFinishNode_KeepsLocalOutputBytesBesideTheStore(t *testing.T) {
	s := storetest.Open(t)
	s.SetOutputDir(t.TempDir())
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{ID: "run-local", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-local", NodeID: "n", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNode(ctx, "run-local", "n", "success", "", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNodeOutput(ctx, "run-local", "n")
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("output = %s, %v", got, err)
	}
	n, _ := s.GetNode(ctx, "run-local", "n")
	if err := os.WriteFile(store.OutputPath(s.OutputDir(), n.OutputRef.Key), []byte(`{"ok":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNodeOutput(ctx, "run-local", "n"); err == nil {
		t.Fatal("a tampered output file read back without error")
	}
	s.SetOutputDir("")
	if err := s.FinishNode(ctx, "run-local", "n", "success", "", []byte(`1`)); !errors.Is(err, store.ErrNoOutputDir) {
		t.Fatalf("output bytes on a store with no output dir: err = %v", err)
	}
}

func TestFinishNode_HoldsALocalRunToItsOutputLimit(t *testing.T) {
	s := storetest.Open(t)
	s.SetOutputDir(t.TempDir())
	ctx := t.Context()
	if err := s.CreateRun(ctx, store.Run{ID: "run-full", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"big", "last"} {
		if err := s.CreateNode(ctx, store.Node{RunID: "run-full", NodeID: n, Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range limits.RunOutputBytes / limits.NodeOutputBytes {
		key, _ := s.NodeOutputKey(ctx, "run-full", "big")
		ref := store.OutputRef{Key: key, Size: limits.NodeOutputBytes, SHA256: fmt.Sprintf("%064x", i)}
		if err := s.RecordLocalOutput(ctx, "run-full", "big", ref, time.Now()); err != nil {
			t.Fatalf("output %d within the run's 1 GiB: %v", i, err)
		}
	}
	err := s.FinishNode(ctx, "run-full", "last", "success", "", []byte(`1`))
	if !errors.Is(err, store.ErrOutputLimit) || !errors.Is(err, store.ErrOutputNotStored) {
		t.Fatalf("a byte past the run's 1 GiB: err = %v, want the run limit", err)
	}
}

func TestGetNodeOutput_ReportsBadOrMissingBytes(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	if err := s.CreateRun(ctx, store.Run{ID: "run-a", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "n", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNode(ctx, "run-a", "n", "success", "", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	n, _ := s.GetNode(ctx, "run-a", "n")
	path := store.OutputPath(s.OutputDir(), n.OutputRef.Key)
	if err := os.WriteFile(path, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := s.GetNodeOutput(ctx, "run-a", "n"); !errors.Is(err, store.ErrOutputCorrupt) {
		t.Fatalf("a changed output file = %s, %v; want it refused as corrupt", out, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if out, err := s.GetNodeOutput(ctx, "run-a", "n"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a lost output file = %s, %v; want not found", out, err)
	}
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
