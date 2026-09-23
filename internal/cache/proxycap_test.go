package cache

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func backdate(t *testing.T, path string, age time.Duration) {
	t.Helper()
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func seedProxyEntry(t *testing.T, registry, key string, size int, age time.Duration) {
	t.Helper()
	dir := filepath.Join(proxyDir, registry)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for ext, n := range map[string]int{".body": size, ".meta": 10} {
		path := filepath.Join(dir, key+ext)
		if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		backdate(t, path, age)
	}
}

func TestTheProxyCapEvictsTheLeastRecentlyServedEntries(t *testing.T) {
	savedDir, savedCap := proxyDir, proxyMaxBytes
	t.Cleanup(func() { proxyDir, proxyMaxBytes = savedDir, savedCap })
	proxyDir = t.TempDir()
	seedProxyEntry(t, "npm", "oldest", 90, 3*time.Hour)
	seedProxyEntry(t, "pypi", "middle", 90, 2*time.Hour)
	seedProxyEntry(t, "npm", "newest", 90, time.Hour)

	// safety: negative control, no cap evicts nothing.
	proxyMaxBytes = 0
	if evicted, _ := enforceProxyCap(); evicted != 0 {
		t.Fatalf("an unset cap evicted %d entries", evicted)
	}
	proxyMaxBytes = 150
	evicted, freed := enforceProxyCap()
	if evicted != 2 || freed != 200 {
		t.Fatalf("evicted %d entries of %d bytes, want the two oldest of 200", evicted, freed)
	}
	for _, gone := range []string{filepath.Join("npm", "oldest.body"), filepath.Join("pypi", "middle.meta")} {
		if _, err := os.Stat(filepath.Join(proxyDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(proxyDir, "npm", "newest.body")); err != nil {
		t.Errorf("the newest entry was evicted: %v", err)
	}
	if got := proxyBytes.Load(); got != 100 {
		t.Fatalf("running total = %d, want the 100 bytes left", got)
	}
}

// The registry proxy is cluster-internal and answers runner pods that carry
// no credential, so it takes none; the daily egress cap is what bounds a
// caller churning it, and past the cap the proxy refuses like every other
// metered download.
func TestTheRegistryProxyIsOpenAndBoundedByTheEgressCap(t *testing.T) {
	srv := newBudgetedServer(t, "s3cret", egress.Config{GlobalDailyCapBytes: 200})
	if got := get(t, srv, "/proxy/no-such-registry/pkg", ""); got.status != http.StatusBadRequest {
		t.Fatalf("an anonymous proxy request under the cap = %d, want the handler's 400", got.status)
	}
	seedArtifact(t, "job1", "out.tar", 100)
	for range 2 {
		if got := get(t, srv, artifactDownloadPath, "s3cret"); got.status != http.StatusOK {
			t.Fatalf("download under the cap = %d", got.status)
		}
	}
	if got := get(t, srv, "/proxy/npm/left-pad", ""); got.status != http.StatusTooManyRequests {
		t.Fatalf("an anonymous proxy request past the daily cap = %d, want 429", got.status)
	}
}
