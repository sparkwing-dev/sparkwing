package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type stubNodeReader struct{ nodes map[string]*store.Node }

func (s stubNodeReader) GetNode(_ context.Context, _, nodeID string) (*store.Node, error) {
	return s.nodes[nodeID], nil
}

// publishProducer captures globs from a fresh producer workspace into
// art and returns a node reader that maps producerID to the recorded
// manifest digest, mirroring what executeNode persists on a producer.
func publishProducer(t *testing.T, art storage.ArtifactStore, producerID string, files map[string][]byte) stubNodeReader {
	t.Helper()
	ws := t.TempDir()
	for rel, data := range files {
		writeArtifactFile(t, ws, rel, data, 0o644)
	}
	digest, err := captureArtifacts(context.Background(), art, ws, []string{"**"})
	if err != nil {
		t.Fatalf("captureArtifacts: %v", err)
	}
	return stubNodeReader{nodes: map[string]*store.Node{
		producerID: {NodeID: producerID, ArtifactManifest: digest},
	}}
}

func readStagedFile(t *testing.T, ws, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read staged %s: %v", rel, err)
	}
	return string(b)
}

func TestStageConsumedArtifacts_WritesAtDeclaredPaths(t *testing.T) {
	art := newTestArtifactStore(t)
	reader := publishProducer(t, art, "build", map[string][]byte{
		"dist/a.txt":     []byte("alpha"),
		"dist/sub/b.txt": []byte("bravo"),
	})
	consumerWS := t.TempDir()

	n, err := stageConsumedArtifacts(context.Background(), art, reader, "run-1", consumerWS,
		[]sparkwing.ConsumeEdge{{Producer: "build"}})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if n != 2 {
		t.Fatalf("staged %d files, want 2", n)
	}
	if got := readStagedFile(t, consumerWS, "dist/a.txt"); got != "alpha" {
		t.Errorf("dist/a.txt = %q", got)
	}
	if got := readStagedFile(t, consumerWS, "dist/sub/b.txt"); got != "bravo" {
		t.Errorf("dist/sub/b.txt = %q", got)
	}
}

func TestStageConsumedArtifacts_IntoPrefixPreservesStructure(t *testing.T) {
	art := newTestArtifactStore(t)
	reader := publishProducer(t, art, "build", map[string][]byte{"dist/a.txt": []byte("alpha")})
	consumerWS := t.TempDir()

	if _, err := stageConsumedArtifacts(context.Background(), art, reader, "run-1", consumerWS,
		[]sparkwing.ConsumeEdge{{Producer: "build", Into: "vendor/build"}}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if got := readStagedFile(t, consumerWS, "vendor/build/dist/a.txt"); got != "alpha" {
		t.Errorf("staged content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(consumerWS, "dist", "a.txt")); !os.IsNotExist(err) {
		t.Errorf("file should not land at the un-prefixed path")
	}
}

func TestStageConsumedArtifacts_OverwritesExistingFile(t *testing.T) {
	art := newTestArtifactStore(t)
	reader := publishProducer(t, art, "build", map[string][]byte{"dist/a.txt": []byte("fresh")})
	consumerWS := t.TempDir()
	writeArtifactFile(t, consumerWS, "dist/a.txt", []byte("stale"), 0o600)

	if _, err := stageConsumedArtifacts(context.Background(), art, reader, "run-1", consumerWS,
		[]sparkwing.ConsumeEdge{{Producer: "build"}}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if got := readStagedFile(t, consumerWS, "dist/a.txt"); got != "fresh" {
		t.Errorf("overwrite: got %q, want fresh", got)
	}
}

func TestStageConsumedArtifacts_PreservesMode(t *testing.T) {
	art := newTestArtifactStore(t)
	ws := t.TempDir()
	writeArtifactFile(t, ws, "bin/run.sh", []byte("#!/bin/sh\n"), 0o755)
	digest, err := captureArtifacts(context.Background(), art, ws, []string{"**"})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	reader := stubNodeReader{nodes: map[string]*store.Node{
		"build": {NodeID: "build", ArtifactManifest: digest},
	}}
	consumerWS := t.TempDir()

	if _, err := stageConsumedArtifacts(context.Background(), art, reader, "run-1", consumerWS,
		[]sparkwing.ConsumeEdge{{Producer: "build"}}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	info, err := os.Stat(filepath.Join(consumerWS, "bin", "run.sh"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 755", info.Mode().Perm())
	}
}

func TestStageConsumedArtifacts_MissingManifestIsNoOp(t *testing.T) {
	art := newTestArtifactStore(t)
	reader := stubNodeReader{nodes: map[string]*store.Node{
		"build": {NodeID: "build", ArtifactManifest: ""},
	}}
	consumerWS := t.TempDir()

	n, err := stageConsumedArtifacts(context.Background(), art, reader, "run-1", consumerWS,
		[]sparkwing.ConsumeEdge{{Producer: "build"}})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if n != 0 {
		t.Errorf("staged %d files, want 0", n)
	}
}

func TestStageConsumedArtifacts_RejectsEscapingPath(t *testing.T) {
	art := newTestArtifactStore(t)
	reader := publishProducer(t, art, "build", map[string][]byte{"a.txt": []byte("x")})
	consumerWS := t.TempDir()

	_, err := stageConsumedArtifacts(context.Background(), art, reader, "run-1", consumerWS,
		[]sparkwing.ConsumeEdge{{Producer: "build", Into: "../escape"}})
	if err == nil {
		t.Fatal("expected error for path escaping the workspace")
	}
}

func TestResolveArtifactStoreFromEnv_OpensURL(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "fs://"+t.TempDir())
	s, err := resolveArtifactStoreFromEnv(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if s == nil {
		t.Fatal("expected a store for a set fs:// URL")
	}
}

func TestResolveArtifactStoreFromEnv_BadURLErrors(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "nonsense://x")
	if _, err := resolveArtifactStoreFromEnv(context.Background()); err == nil {
		t.Fatal("expected an error for an unsupported scheme")
	}
}

func TestStageConsumedArtifacts_LastEdgeWinsOnOverlap(t *testing.T) {
	art := newTestArtifactStore(t)
	first := publishProducer(t, art, "first", map[string][]byte{"shared.txt": []byte("from-first")})
	second := publishProducer(t, art, "second", map[string][]byte{"shared.txt": []byte("from-second")})
	reader := stubNodeReader{nodes: map[string]*store.Node{
		"first":  first.nodes["first"],
		"second": second.nodes["second"],
	}}
	consumerWS := t.TempDir()

	if _, err := stageConsumedArtifacts(context.Background(), art, reader, "run-1", consumerWS,
		[]sparkwing.ConsumeEdge{{Producer: "first"}, {Producer: "second"}}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if got := readStagedFile(t, consumerWS, "shared.txt"); got != "from-second" {
		t.Errorf("overlap: got %q, want from-second (last edge wins)", got)
	}
}
