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
if [ "$1" = list ]; then exit 0; fi
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
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing"))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	ctx := gotoolchain.WithSession(t.Context(), os.Environ(), nil)
	for _, wantSource := range []string{"compiled", "cached"} {
		lease, published, err := bincache.MaterializePipeline(ctx, dir, bincache.CompileOptions{Environment: os.Environ(), Diagnostics: os.Stderr})
		if err != nil {
			t.Fatal(err)
		}
		binary, verifyErr := lease.Install(filepath.Join(t.TempDir(), "verified"))
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		data, readErr := os.ReadFile(binary.Path())
		releaseErr := lease.Release()
		if readErr != nil || releaseErr != nil || published != (wantSource == "compiled") || string(data) != "#!/bin/sh\nexit 0\n" {
			t.Fatalf("local materialization: published=%t data=%q errors=%v,%v", published, data, readErr, releaseErr)
		}
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	builds, err := os.ReadFile(buildLog)
	if err != nil || string(builds) != "build\n" || calls != 0 {
		t.Fatalf("builds=%q network=%d err=%v", builds, calls, err)
	}
}
