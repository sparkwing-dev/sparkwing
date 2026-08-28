package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/localws"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func TestApplyDashboardProfile_FillsStoresFromSurfaces(t *testing.T) {
	dir := t.TempDir()
	writeProfilesFixture(t, "profiles:\n  bucket:\n    logs: { type: filesystem, path: "+filepath.Join(dir, "logs")+" }\n    cache: { type: filesystem, path: "+filepath.Join(dir, "cache")+" }\n")

	var opts localws.Options
	if err := applyDashboardProfile(context.Background(), &opts, "bucket"); err != nil {
		t.Fatalf("applyDashboardProfile: %v", err)
	}
	if opts.LogStore == nil {
		t.Error("LogStore: got nil, want the profile's logs surface")
	}
	if opts.ArtifactStore == nil {
		t.Error("ArtifactStore: got nil, want the profile's cache surface")
	}
	if opts.LogStoreLabel != "filesystem" {
		t.Errorf("LogStoreLabel: got %q, want filesystem", opts.LogStoreLabel)
	}
}

func TestApplyDashboardProfile_ExplicitStoreWins(t *testing.T) {
	dir := t.TempDir()
	writeProfilesFixture(t, "profiles:\n  bucket:\n    logs: { type: filesystem, path: "+filepath.Join(dir, "logs")+" }\n")

	explicit, err := fs.NewLogStore(filepath.Join(dir, "explicit"))
	if err != nil {
		t.Fatal(err)
	}
	opts := localws.Options{LogStore: explicit, LogStoreLabel: "fs"}
	if err := applyDashboardProfile(context.Background(), &opts, "bucket"); err != nil {
		t.Fatalf("applyDashboardProfile: %v", err)
	}
	if opts.LogStore != storage.LogStore(explicit) {
		t.Error("the explicitly opened log store was replaced by the profile's")
	}
	if opts.LogStoreLabel != "fs" {
		t.Errorf("LogStoreLabel: got %q, want the explicit fs", opts.LogStoreLabel)
	}
}

func TestApplyDashboardProfile_NoProfileIsANoOp(t *testing.T) {
	var opts localws.Options
	if err := applyDashboardProfile(context.Background(), &opts, ""); err != nil {
		t.Fatalf("applyDashboardProfile: %v", err)
	}
	if opts.LogStore != nil || opts.ArtifactStore != nil {
		t.Error("no profile named, so no store should have been opened")
	}
}
