//go:build !windows

package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
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
	key, parts, err := bincache.ExplainCacheKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := gotoolchain.WithSession(t.Context(), os.Environ(), nil)
	for _, wantSource := range []string{"compiled", "cached"} {
		lease, source, err := pipelineBinary(ctx, dir, key, parts, os.Environ())
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
	result, err := warmLocalPipeline(t.Context(), dir, []string{"PATH=" + bin, "INSTALL_MARKER=" + marker})
	if err != nil {
		t.Fatal(err)
	}
	called, err := os.ReadFile(marker)
	if err != nil || string(called) != "ci --no-audit --no-fund\n" || result.Language != "typescript" || result.Status != "prepared" || result.Path != filepath.Join(dir, "node_modules") || result.Key != "" {
		t.Fatalf("TypeScript preparation: %+v called=%q err=%v", result, called, err)
	}
}
