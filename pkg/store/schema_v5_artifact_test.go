package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestSchemaV5_UpgradeAddsArtifactManifestColumn reconstructs a runs
// store left at schema 4 by a binary that predates the artifact_manifest
// column (v0.9.x / v0.10.0), then opens it with the current binary and
// asserts the v5 migration adds the column so node reads and artifact
// writes work again. The pre-v5 no-op-at-current-version path left such
// a database without the column, breaking every node read.
func TestSchemaV5_UpgradeAddsArtifactManifestColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema4.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE nodes DROP COLUMN artifact_manifest`); err != nil {
		t.Fatalf("drop artifact_manifest: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version = 5`); err != nil {
		t.Fatalf("reset version to 4: %v", err)
	}
	if v := readSchemaVersion(t, st.DB()); v != 4 {
		t.Fatalf("seeded version = %d, want 4", v)
	}
	if hasArtifactManifestColumn(t, st) {
		t.Fatal("artifact_manifest should be absent before upgrade")
	}
	_ = st.Close()

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()

	if v := readSchemaVersion(t, up.DB()); v != 5 {
		t.Errorf("version after upgrade = %d, want 5", v)
	}
	if !hasArtifactManifestColumn(t, up) {
		t.Fatal("artifact_manifest should be present after upgrade")
	}

	ctx := context.Background()
	if err := up.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := up.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "producer", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	const digest = "sha256:deadbeef"
	if err := up.SetNodeArtifactManifest(ctx, "run-1", "producer", digest); err != nil {
		t.Fatalf("SetNodeArtifactManifest: %v", err)
	}
	n, err := up.GetNode(ctx, "run-1", "producer")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.ArtifactManifest != digest {
		t.Errorf("ArtifactManifest = %q, want %q", n.ArtifactManifest, digest)
	}
}

func hasArtifactManifestColumn(t *testing.T, s *store.Store) bool {
	t.Helper()
	rows, err := s.DB().Query(`PRAGMA table_info(nodes)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(nodes): %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "artifact_manifest" {
			return true
		}
	}
	return false
}
