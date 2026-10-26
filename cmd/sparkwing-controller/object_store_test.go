package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
)

// A multi-team controller enforces free allowances only over the object
// store, so it refuses to start without one; a single-team install keeps its
// disk-backed stores.
func TestAMultiTeamControllerRequiresAnObjectStore(t *testing.T) {
	multi, single := deploymenttest.Team(t), deployment.Config{}
	err := checkMultiTeamObjectStore(multi, "", "")
	if err == nil || !strings.Contains(err.Error(), "--bucket-store") {
		t.Fatalf("multi-team with no object store = %v, want a refusal naming --bucket-store", err)
	}
	if err := checkMultiTeamObjectStore(multi, "s3://bucket/prefix", ""); err != nil {
		t.Fatalf("multi-team with an object store: %v", err)
	}
	if err := checkMultiTeamObjectStore(single, "", ""); err != nil {
		t.Fatalf("single-team with no object store: %v", err)
	}
}

// The hosted controller's blobs must live in S3: its pod home is ephemeral
// while PostgreSQL keeps their records, and a bucket-measurement URL stores
// nothing.
func TestTheHostedControllerRequiresAnS3BlobStore(t *testing.T) {
	cloud := deploymenttest.Cloud(t, "https://api.example.com")
	err := checkMultiTeamObjectStore(cloud, "s3://bucket/prefix", "")
	if err == nil || !strings.Contains(err.Error(), "--cache-blob-store") {
		t.Fatalf("hosted with only a bucket measurement = %v, want a refusal naming --cache-blob-store", err)
	}
	if err := checkMultiTeamObjectStore(cloud, "s3://bucket/prefix", "s3://bucket/prefix"); err != nil {
		t.Fatalf("hosted with an S3 blob store: %v", err)
	}
}
