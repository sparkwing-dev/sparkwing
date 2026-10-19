package bincache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedGoBuildSettingsSeparateCacheEntries(t *testing.T) {
	dir := newPipelineDir(t)
	base := []string{"GOWORK=off", "GOFLAGS=-tags=first", "CONTROLLER_TOKEN=private"}
	first, _, err := ExplainCacheKey(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{"GOFLAGS=-tags=second", "GOARCH=arm64", "CGO_ENABLED=0", "GOTOOLCHAIN=go1.26.8", "GOSUMDB=off", "GONOSUMDB=example.test", "GOINSECURE=example.test", "GOPRIVATE=example.test", "GONOPROXY=example.test"} {
		other, _, err := ExplainCacheKey(dir, append(append([]string(nil), base...), replacement))
		if err != nil || other == first {
			t.Fatalf("build setting %s: key=%s error=%v", replacement, other, err)
		}
	}
	same, _, err := ExplainCacheKey(dir, append(base, "CONTROLLER_TOKEN=different", "SPARKWING_RUN_ID=another-run"))
	if err != nil || same != first {
		t.Fatalf("runtime authority affected compile identity: key=%s error=%v", same, err)
	}
}

func TestCapturedGoWorkSelectsHashedDependencies(t *testing.T) {
	dir := newWorkspaceAt(t, "go 1.26\nuse ./svc\nuse ./tmpl\n")
	work := filepath.Join(filepath.Dir(dir), "go.work")
	env := []string{"GOWORK=" + work}
	t.Setenv("GOWORK", "off")
	first, _, err := ExplainCacheKey(dir, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "tmpl", "registry.go"), []byte("package tmpl\nconst Value = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other, _, err := ExplainCacheKey(dir, env)
	if err != nil || other == first {
		t.Fatalf("captured workspace dependency did not invalidate binary: %s %v", other, err)
	}
}
