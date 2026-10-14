package orchestrator

import (
	"context"
	"testing"
)

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
