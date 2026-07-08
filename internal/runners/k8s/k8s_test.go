package k8s

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

func jobEnv(t *testing.T, cfg Config) map[string]string {
	t.Helper()
	r := &Runner{cfg: cfg}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"})
	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(containers))
	}
	out := map[string]string{}
	for _, e := range containers[0].Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestBuildJob_StampsArtifactStoreURLWhenSet(t *testing.T) {
	env := jobEnv(t, Config{Image: "img", ArtifactStoreURL: "s3://bucket/prefix"})
	if got := env["SPARKWING_CACHE_URL"]; got != "s3://bucket/prefix" {
		t.Fatalf("SPARKWING_CACHE_URL = %q, want s3://bucket/prefix", got)
	}
}

func TestBuildJob_OmitsArtifactStoreURLWhenEmpty(t *testing.T) {
	env := jobEnv(t, Config{Image: "img"})
	if _, ok := env["SPARKWING_CACHE_URL"]; ok {
		t.Fatalf("SPARKWING_CACHE_URL should be absent when ArtifactStoreURL is empty")
	}
}
