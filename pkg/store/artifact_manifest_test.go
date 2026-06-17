package store_test

import (
	"context"
	"testing"
)

func TestSetNodeArtifactManifest_RoundTrips(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	if err := s.SetNodeArtifactManifest(ctx, "run-1", "node-a", "sha-deadbeef"); err != nil {
		t.Fatalf("SetNodeArtifactManifest: %v", err)
	}
	n, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.ArtifactManifest != "sha-deadbeef" {
		t.Fatalf("artifact_manifest: got %q want sha-deadbeef", n.ArtifactManifest)
	}
}

func TestSetNodeArtifactManifest_SurvivesFinishNode(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	if err := s.SetNodeArtifactManifest(ctx, "run-1", "node-a", "m1"); err != nil {
		t.Fatalf("SetNodeArtifactManifest: %v", err)
	}
	if err := s.FinishNode(ctx, "run-1", "node-a", "success", "", []byte(`"ok"`)); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	n, _ := s.GetNode(ctx, "run-1", "node-a")
	if n.ArtifactManifest != "m1" {
		t.Fatalf("manifest cleared by FinishNode: got %q", n.ArtifactManifest)
	}
	if n.Status != "done" {
		t.Fatalf("status: %q", n.Status)
	}
}

func TestNode_DefaultsToEmptyManifest(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	n, _ := s.GetNode(ctx, "run-1", "node-a")
	if n.ArtifactManifest != "" {
		t.Fatalf("fresh node should have empty manifest, got %q", n.ArtifactManifest)
	}
}
