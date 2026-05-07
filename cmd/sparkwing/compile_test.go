package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/bincache"
)

// scaffoldPipelineRepo lays out a minimal two-module tree under root:
func scaffoldPipelineRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk")
	pipDir := filepath.Join(root, "pipelines")
	for _, d := range []string{sdkDir, pipDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	writeFile(t, filepath.Join(sdkDir, "go.mod"), "module example.com/sdk\n\ngo 1.22\n")
	writeFile(t, filepath.Join(sdkDir, "sdk.go"), "package sdk\n\nconst Version = \"1\"\n")

	writeFile(t, filepath.Join(pipDir, "go.mod"),
		"module example.com/pipelines\n\ngo 1.22\n\nrequire example.com/sdk v0.0.0\n\nreplace example.com/sdk => ../sdk\n")
	writeFile(t, filepath.Join(pipDir, "main.go"), "package main\n\nfunc main() {}\n")

	return pipDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineCacheKey_StableAcrossRuns(t *testing.T) {
	pipDir := scaffoldPipelineRepo(t)
	k1, err := bincache.PipelineCacheKey(pipDir)
	if err != nil {
		t.Fatalf("pipelineCacheKey: %v", err)
	}
	k2, err := bincache.PipelineCacheKey(pipDir)
	if err != nil {
		t.Fatalf("pipelineCacheKey (repeat): %v", err)
	}
	if k1 != k2 {
		t.Fatalf("cache key should be stable: %q vs %q", k1, k2)
	}
	// Format is `aaaaaaaa-bbbbbbbb` so it matches the cache server's
	// /bin/<hash> regex; 17 chars = 16 hex + 1 hyphen.
	if len(k1) != 17 {
		t.Fatalf("cache key should be 17 chars (two 8-hex segments + hyphen), got %d", len(k1))
	}
}

func TestPipelineCacheKey_ChangesWhenPipelineSourceChanges(t *testing.T) {
	pipDir := scaffoldPipelineRepo(t)
	k1, _ := bincache.PipelineCacheKey(pipDir)

	// Bump mtime + size of main.go. Size delta ensures the hash
	// changes even if mtime resolution swallows the touch.
	writeFile(t, filepath.Join(pipDir, "main.go"),
		"package main\n\nfunc main() { println(\"hi\") }\n")
	// Guarantee a fresh mtime on filesystems with second-level resolution.
	time.Sleep(10 * time.Millisecond)

	k2, _ := bincache.PipelineCacheKey(pipDir)
	if k1 == k2 {
		t.Fatal("cache key did not change after editing main.go")
	}
}

func TestPipelineCacheKey_ChangesWhenReplaceTargetChanges(t *testing.T) {
	pipDir := scaffoldPipelineRepo(t)
	k1, _ := bincache.PipelineCacheKey(pipDir)

	// Edit the SDK (via replace). Pipeline binary depends on it, so
	// the key must change.
	sdkFile := filepath.Join(filepath.Dir(pipDir), "sdk", "sdk.go")
	writeFile(t, sdkFile, "package sdk\n\nconst Version = \"2\"\n// extra\n")
	time.Sleep(10 * time.Millisecond)

	k2, _ := bincache.PipelineCacheKey(pipDir)
	if k1 == k2 {
		t.Fatal("cache key did not change after editing replace target")
	}
}

func TestPipelineCacheKey_IgnoresNonGoFilesInReplaceTarget(t *testing.T) {
	pipDir := scaffoldPipelineRepo(t)
	k1, _ := bincache.PipelineCacheKey(pipDir)

	// Add a README to the replace target. Not Go source, so it must
	// not bust the cache (would cause spurious rebuilds from
	// unrelated file edits).
	sdkReadme := filepath.Join(filepath.Dir(pipDir), "sdk", "README.md")
	writeFile(t, sdkReadme, "hello world")

	k2, _ := bincache.PipelineCacheKey(pipDir)
	if k1 != k2 {
		t.Fatalf("README change should not bust cache: %q vs %q", k1, k2)
	}
}

// newSparksFixture lays out a consumer sparkwing dir with a go.mod and
// an optional sparks.yaml at the given manifest path. Returns the dir.
func newSparksFixture(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"),
		"module example.com/consumer\n\ngo 1.22\n")
	if manifest != "" {
		writeFile(t, filepath.Join(dir, "sparks.yaml"), manifest)
	}
	return dir
}

// newProxy spins a mock go module proxy that serves @latest -> `version`
// for any requested module.
func newProxy(t *testing.T, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/@latest") {
			fmt.Fprintf(w, `{"Version":%q,"Time":"2026-04-22T00:00:00Z"}`, version)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pointResolverAt sets GOPROXY to the mock server so the resolver's
// default client reaches it. GOPRIVATE cleared so every module goes
// through the proxy path.
func pointResolverAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Setenv("GOPROXY", srv.URL)
	t.Setenv("GOPRIVATE", "")
	// Ensure `go mod download` (invoked inside WriteOverlay) is a
	// no-op -- we're testing the resolve + overlay write behavior,
	// not the downloader.
	fakeGo(t)
}

// fakeGo installs a shell script that acts as `go` and touches the
// .sum file when `-modfile=...` is passed. Matches the script used in
// internal/sparks/overlay_test.go.
func fakeGo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in\n" +
		"    -modfile=*)\n" +
		"      mod=\"${arg#-modfile=}\"\n" +
		"      sum=\"${mod%.mod}.sum\"\n" +
		"      : > \"$sum\"\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	bin := filepath.Join(dir, "go")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKS_GO_BIN", bin)
}

func TestResolveSparks_NoManifest_FastPath(t *testing.T) {
	// With no sparks.yaml, resolveSparks must not touch the network
	// nor write any overlay. Point at an invalid proxy URL -- if a
	// network call happens it'll surface as an error.
	t.Setenv("GOPROXY", "http://proxy.invalid.example")
	dir := newSparksFixture(t, "")
	if err := resolveSparks(context.Background(), dir, compileOptions{}); err != nil {
		t.Fatalf("fast path should be a no-op, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".resolved.mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fast path should not write overlay, stat err: %v", err)
	}
}

func TestResolveSparks_WithManifest_WritesOverlay(t *testing.T) {
	srv := newProxy(t, "v0.10.3")
	pointResolverAt(t, srv)

	manifest := "libraries:\n  - name: sparks-core\n    source: example.com/sparks-core\n    version: latest\n"
	dir := newSparksFixture(t, manifest)

	if err := resolveSparks(context.Background(), dir, compileOptions{}); err != nil {
		t.Fatalf("resolveSparks: %v", err)
	}
	overlayBytes, err := os.ReadFile(filepath.Join(dir, ".resolved.mod"))
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if !strings.Contains(string(overlayBytes), "example.com/sparks-core v0.10.3") {
		t.Fatalf("overlay missing resolved version; got:\n%s", overlayBytes)
	}
}

func TestResolveSparks_NoUpdate_SkipsResolve(t *testing.T) {
	// Proxy intentionally broken; --no-update must short-circuit
	// before any network call is attempted.
	t.Setenv("GOPROXY", "http://proxy.invalid.example")
	manifest := "libraries:\n  - name: sparks-core\n    source: example.com/sparks-core\n    version: latest\n"
	dir := newSparksFixture(t, manifest)

	if err := resolveSparks(context.Background(), dir, compileOptions{NoUpdate: true}); err != nil {
		t.Fatalf("--no-update should bypass resolve, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".resolved.mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--no-update should not write overlay, stat err: %v", err)
	}
}

func TestResolveSparks_EnvVar_SkipsResolve(t *testing.T) {
	t.Setenv("GOPROXY", "http://proxy.invalid.example")
	t.Setenv("SPARKWING_NO_SPARKS_RESOLVE", "1")
	manifest := "libraries:\n  - name: sparks-core\n    source: example.com/sparks-core\n    version: latest\n"
	dir := newSparksFixture(t, manifest)

	if err := resolveSparks(context.Background(), dir, compileOptions{}); err != nil {
		t.Fatalf("SPARKWING_NO_SPARKS_RESOLVE should bypass resolve, got: %v", err)
	}
}

func TestResolveSparks_ProxyDown_FailsLoudly(t *testing.T) {
	// No --no-update set. Broken proxy should surface as a compile
	// error so an agent wanting `latest` knows the resolve failed
	// instead of silently pinning to stale go.mod versions.
	t.Setenv("GOPROXY", "http://127.0.0.1:1")
	t.Setenv("GOPRIVATE", "")
	manifest := "libraries:\n  - name: sparks-core\n    source: example.com/sparks-core\n    version: latest\n"
	dir := newSparksFixture(t, manifest)

	err := resolveSparks(context.Background(), dir, compileOptions{})
	if err == nil {
		t.Fatal("expected resolve failure when proxy is down")
	}
	if !strings.Contains(err.Error(), "--no-update") {
		t.Fatalf("error should hint at --no-update, got: %v", err)
	}
}

func TestPipelineCacheKey_PipelineDirTreatsAllFilesAsInputs(t *testing.T) {
	// The pipeline module itself gets full-tree hashing because
	// pipelines.yaml and other non-Go files can drive behavior.
	pipDir := scaffoldPipelineRepo(t)
	k1, _ := bincache.PipelineCacheKey(pipDir)

	writeFile(t, filepath.Join(pipDir, "pipelines.yaml"), "pipelines: []\n")

	k2, _ := bincache.PipelineCacheKey(pipDir)
	if k1 == k2 {
		t.Fatal("pipelines.yaml change should bust cache")
	}
}
