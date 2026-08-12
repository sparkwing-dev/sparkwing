package orchestrator_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type artifactProducerPipe struct{ sparkwing.Base }

func (artifactProducerPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "produce", func(_ context.Context) error {
		dir := filepath.Join(sparkwing.WorkDir(), "dist")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "out.txt"), []byte("artifact-bytes"), 0o644)
	}).
		Outputs("dist/**").
		Cache(func(_ context.Context) sparkwing.CacheKey { return sparkwing.Key("produce", "v1") })
	return nil
}

func init() {
	register("artifact-producer", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &artifactProducerPipe{} })
}

func findNode(t *testing.T, st *store.Store, runID, nodeID string) *store.Node {
	t.Helper()
	nodes, err := st.ListNodes(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if n.NodeID == nodeID {
			return n
		}
	}
	t.Fatalf("node %q not found in run %s", nodeID, runID)
	return nil
}

// TestArtifacts_CapturedThenReplayedOnCacheHit runs a producer that
// declares Outputs, asserts its files are captured into a manifest on
// the node, then re-runs so the producer cache-hits and asserts the
// manifest reference is copied onto the replayed node without
// re-executing it.
func TestArtifacts_CapturedThenReplayedOnCacheHit(t *testing.T) {
	ws := t.TempDir()
	orig := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(ws)
	t.Cleanup(func() { sparkwing.SetWorkDir(orig) })

	art, err := fsstore.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	p := newPaths(t)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	res1, err := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, art), orchestrator.Options{Pipeline: "artifact-producer"})
	if err != nil || res1.Status != "success" {
		t.Fatalf("run 1: status=%v err=%v", res1, err)
	}
	prod1 := findNode(t, st, res1.RunID, "produce")
	if prod1.ArtifactManifest == "" {
		t.Fatal("producer node recorded no artifact manifest")
	}

	m := readManifestFromStore(t, art, prod1.ArtifactManifest)
	if len(m.Entries) != 1 || m.Entries[0].Path != "dist/out.txt" {
		t.Fatalf("unexpected manifest entries: %+v", m.Entries)
	}
	if got := readBlob(t, art, m.Entries[0].Digest); got != "artifact-bytes" {
		t.Fatalf("blob content: got %q", got)
	}

	res2, err := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, art), orchestrator.Options{Pipeline: "artifact-producer"})
	if err != nil || res2.Status != "success" {
		t.Fatalf("run 2: status=%v err=%v", res2, err)
	}
	prod2 := findNode(t, st, res2.RunID, "produce")
	if prod2.Outcome != string(sparkwing.Cached) {
		t.Fatalf("run 2 producer outcome = %q, want cached", prod2.Outcome)
	}
	if prod2.ArtifactManifest != prod1.ArtifactManifest {
		t.Fatalf("cache hit did not copy manifest: run2=%q run1=%q", prod2.ArtifactManifest, prod1.ArtifactManifest)
	}
}

// readManifestFromStore + readBlob mirror the in-package helpers but
// operate on the exported manifest JSON shape so the external test can
// read what the orchestrator wrote.
type e2eManifest struct {
	Entries []struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
		Mode   uint32 `json:"mode"`
	} `json:"entries"`
}

func readManifestFromStore(t *testing.T, art storage.ArtifactStore, digest string) e2eManifest {
	t.Helper()
	rc, err := art.Get(context.Background(), "artifacts/manifests/"+digest)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	var m e2eManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return m
}

func readBlob(t *testing.T, art storage.ArtifactStore, digest string) string {
	t.Helper()
	rc, err := art.Get(context.Background(), "artifacts/blobs/"+digest)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	return string(b)
}
