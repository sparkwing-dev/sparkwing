package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestNodeIsAncestor_FollowsDepsTransitively(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	if err := s.CreateRun(ctx, store.Run{ID: "run-anc", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for id, deps := range map[string][]string{"a": nil, "b": {"a"}, "c": {"b"}, "d": nil} {
		if err := s.CreateNode(ctx, store.Node{RunID: "run-anc", NodeID: id, Status: "pending", Deps: deps}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		ancestor, node string
		want           bool
	}{
		{"a", "c", true}, {"b", "c", true}, {"d", "c", false}, {"c", "a", false}, {"c", "c", false},
	}
	for _, tc := range cases {
		got, err := s.NodeIsAncestor(ctx, store.DefaultTeam, "run-anc", tc.ancestor, tc.node)
		if err != nil || got != tc.want {
			t.Errorf("NodeIsAncestor(%s, %s) = %v, %v; want %v", tc.ancestor, tc.node, got, err, tc.want)
		}
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
	for i := range store.MaxRunOutputBytes / store.MaxOutputBytes {
		key, _ := s.NodeOutputKey(ctx, "run-full", "big")
		ref := store.OutputRef{Key: key, Size: store.MaxOutputBytes, SHA256: fmt.Sprintf("%064x", i)}
		if err := s.RecordLocalOutput(ctx, "run-full", "big", ref, time.Now()); err != nil {
			t.Fatalf("output %d within the run's 1 GiB: %v", i, err)
		}
	}
	err := s.FinishNode(ctx, "run-full", "last", "success", "", []byte(`1`))
	if !errors.Is(err, store.ErrOutputLimit) || !errors.Is(err, store.ErrOutputNotStored) {
		t.Fatalf("a byte past the run's 1 GiB: err = %v, want the run limit", err)
	}
}

func TestWriteOutputFileDurably_KeepsAMatchingFileAndReplacesAnother(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte(`"same"`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := store.WriteOutputFileDurably(path, []byte(`"same"`)); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !os.SameFile(before, after) {
		t.Fatal("a file that already matched was replaced instead of synced in place")
	}
	if err := store.WriteOutputFileDurably(path, []byte(`"other"`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != `"other"` {
		t.Fatalf("a mismatched file = %s, want it replaced", got)
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
