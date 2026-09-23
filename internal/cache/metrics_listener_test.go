package cache

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newServerForListeners(t *testing.T, metricsAddr string) *Server {
	t.Helper()
	saved := []*string{&dataRoot, &repoDir, &archDir, &artifactsDir, &binsDir, &cacheDir, &uploadsDir, &namesFile, &proxyDir, &sshKeyDir, &apiToken, &teamsDir}
	values := make([]string, len(saved))
	for i, p := range saved {
		values[i] = *p
	}
	t.Cleanup(func() {
		for i, p := range saved {
			*p = values[i]
		}
	})
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = root
	cfg.ProxyDir = filepath.Join(root, "proxy")
	cfg.SSHKeyDir = filepath.Join(root, "no-ssh-key")
	cfg.APIToken = "s3cret"
	cfg.MetricsAddr = metricsAddr
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func status(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code
}

// With --metrics-addr the main listener, the one an ingress publishes, serves
// neither /metrics nor /stats; the metrics listener serves both.
func TestMetricsAddrMovesMetricsAndStatsOffTheMainListener(t *testing.T) {
	s := newServerForListeners(t, "127.0.0.1:0")
	for _, path := range []string{"/metrics", "/stats"} {
		if got := status(t, s.Handler(), path); got != http.StatusNotFound {
			t.Errorf("main listener GET %s = %d, want 404", path, got)
		}
		if got := status(t, s.MetricsHandler(), path); got != http.StatusOK {
			t.Errorf("metrics listener GET %s = %d, want 200", path, got)
		}
	}
	if got := status(t, s.Handler(), "/health"); got != http.StatusOK {
		t.Errorf("main listener GET /health = %d, want 200", got)
	}
}

func TestWithoutMetricsAddrTheMainListenerServesMetrics(t *testing.T) {
	s := newServerForListeners(t, "")
	for _, path := range []string{"/metrics", "/stats"} {
		if got := status(t, s.Handler(), path); got != http.StatusOK {
			t.Errorf("main listener GET %s = %d, want 200", path, got)
		}
	}
	if s.MetricsHandler() != nil {
		t.Error("a server with no --metrics-addr built a metrics listener")
	}
}
