//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestLocalPipelineBinaryBuildsOnceWithoutRemoteTransfers(t *testing.T) {
	dir := scaffoldPipelineRepo(t)
	root := t.TempDir()
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
	t.Setenv("SPARKWING_GITCACHE_URL", "https://removed-cache.example")
	buildLog := filepath.Join(root, "builds")
	t.Setenv("BUILD_LOG", buildLog)
	bin := filepath.Join(root, "go")
	script := `#!/bin/sh
if [ "$1" = env ]; then printf 'auto\ngo1.26.9\noff\n'; exit 0; fi
while [ $# -gt 0 ]; do
  if [ "$1" = -o ]; then
    shift
    printf 'build\n' >> "$BUILD_LOG"
    printf '#!/bin/sh\nexit 0\n' > "$1"
    chmod +x "$1"
    exit 0
  fi
  shift
done
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := 0
	previous := http.DefaultTransport
	http.DefaultTransport = profileTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("missing"))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	key, parts, err := bincache.ExplainCacheKey(dir, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	ctx := gotoolchain.WithSession(t.Context(), os.Environ(), nil)
	for _, wantSource := range []string{"compiled", "cached"} {
		lease, source, err := pipelineBinary(ctx, dir, key, parts, os.Environ(), os.Stderr)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := os.ReadFile(lease.Path())
		releaseErr := lease.Release()
		if readErr != nil || releaseErr != nil || source != wantSource || string(data) != "#!/bin/sh\nexit 0\n" {
			t.Fatalf("local materialization: source=%s data=%q errors=%v,%v", source, data, readErr, releaseErr)
		}
	}
	builds, err := os.ReadFile(buildLog)
	if err != nil || string(builds) != "build\n" || calls != 0 {
		t.Fatalf("builds=%q network=%d err=%v", builds, calls, err)
	}
}

func TestWarmTypeScriptInstallsDependenciesWithoutExecutingPipeline(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"1.0.0"}}`)
	writeFile(t, filepath.Join(dir, "pipeline.ts"), "throw new Error('must not execute')")
	bin := t.TempDir()
	marker := filepath.Join(dir, "npm-invocation")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$INSTALL_MARKER\"\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	result, err := warmLocalPipeline(t.Context(), dir, []string{"PATH=" + bin, "INSTALL_MARKER=" + marker}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	called, err := os.ReadFile(marker)
	if err != nil || string(called) != "ci --no-audit --no-fund\n" || result.Language != "typescript" || result.Status != "prepared" || result.Path != filepath.Join(dir, "node_modules") || result.Key != "" {
		t.Fatalf("TypeScript preparation: %+v called=%q err=%v", result, called, err)
	}
}

func TestCacheWarmTypeScriptMasksNativeInstallerDiagnostics(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"1.0.0"}}`)
	writeFile(t, filepath.Join(dir, "pipeline.ts"), "throw new Error('must not execute')")
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ -n \"$AWS_SECRET_ACCESS_KEY\" ]; then printf 'credential inherited'; exit 2; fi\nprintf 'using alice:p@ss\\n'\nprintf 'alice:p@ss failed\\n' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var out, diagnostics bytes.Buffer
	err := runCacheWarmCommand(t.Context(), []string{"-o", "json"}, &out, cacheWarmDependencies{
		locate:      func() (string, error) { return dir, nil },
		environment: []string{"PATH=" + bin, "HTTPS_PROXY=http://alice:p%40ss@proxy.test", "AWS_SECRET_ACCESS_KEY=private"},
		buildEnv:    procenv.Build, diagnostics: &diagnostics, prepare: warmLocalPipeline,
	})
	if err == nil || !strings.Contains(err.Error(), "npm ci") || out.Len() != 0 || !strings.Contains(diagnostics.String(), "***") || strings.Contains(diagnostics.String(), "alice") || strings.Contains(diagnostics.String(), "p@ss") || strings.Contains(diagnostics.String(), "credential inherited") {
		t.Fatalf("native installer diagnostics: err=%v stdout=%s stderr=%s", err, &out, &diagnostics)
	}
}

func TestCacheWarmGoBuildsThroughSharedCompiler(t *testing.T) {
	installer, body, log := goInstallerFixture(t, false)
	var request struct {
		Directory   string `json:"directory"`
		PipelineDir string `json:"pipeline_dir"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(request.Directory, request.PipelineDir)
	var out, diagnostics bytes.Buffer
	err := runCacheWarmCommand(t.Context(), []string{"-o", "json"}, &out, cacheWarmDependencies{
		locate: func() (string, error) { return dir, nil }, environment: installer.environment,
		buildEnv: procenv.Build, diagnostics: &diagnostics, prepare: warmLocalPipeline,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result cacheWarmResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Language != "go" || result.Status != "prepared" || !filepath.IsAbs(result.Path) || strings.Contains(diagnostics.String(), "p@ss") || strings.Contains(diagnostics.String(), "alice") {
		t.Fatalf("Go warming: result=%+v diagnostics=%q", result, &diagnostics)
	}
	builds, err := os.ReadFile(log)
	if err != nil || string(builds) != "build\n" {
		t.Fatalf("Go warming: builds=%q error=%v", builds, err)
	}
}
