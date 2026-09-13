package cache

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func newBudgetedServer(t *testing.T, token string, cfg egress.Config) *httptest.Server {
	t.Helper()
	savedMeter := egressMeter
	t.Cleanup(func() { egressMeter = savedMeter })

	saved := struct {
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir string
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken        string
	}{
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir,
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken,
	}
	t.Cleanup(func() {
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir = saved.dataRoot, saved.repoDir, saved.archDir, saved.artifactsDir, saved.binsDir, saved.cacheDir
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken = saved.uploadsDir, saved.namesFile, saved.proxyDir, saved.sshKeyDir, saved.apiToken
	})

	root := t.TempDir()
	c := DefaultConfig()
	c.DataDir = root
	c.ProxyDir = filepath.Join(root, "proxy")
	c.SSHKeyDir = filepath.Join(root, "no-ssh-key")
	c.APIToken = token
	c.AllowUnauthenticated = token == ""
	c.EgressMonthlyBytes = cfg.PerPrincipalMonthlyBytes
	c.EgressDailyAlarmBytes = cfg.GlobalDailyAlarmBytes
	s, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.handler)
	t.Cleanup(srv.Close)
	return srv
}

func seedArtifact(t *testing.T, job, name string, size int) {
	t.Helper()
	dir := filepath.Join(artifactsDir, job)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fetched struct {
	status int
	header http.Header
	body   []byte
}

func fetch(t *testing.T, srv *httptest.Server, path, token string) fetched {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return fetched{status: resp.StatusCode, header: resp.Header, body: body}
}

const artifactDownloadPath = "/artifacts/job1?glob=*"

func TestArtifactDownloadsSpendTheCachesMonthlyBudget(t *testing.T) {
	srv := newBudgetedServer(t, "s3cret", egress.Config{PerPrincipalMonthlyBytes: 1})
	seedArtifact(t, "job1", "out.tar", 100)

	first := fetch(t, srv, artifactDownloadPath, "s3cret")
	if first.status != http.StatusOK || len(first.body) == 0 {
		t.Fatalf("first download = %d with %d bytes, want 200 and a body", first.status, len(first.body))
	}
	if state := egressMeter.State(); state.GlobalMonthBytes != int64(len(first.body)) {
		t.Fatalf("metered %d bytes, want the served %d", state.GlobalMonthBytes, len(first.body))
	}

	refused := fetch(t, srv, artifactDownloadPath, "s3cret")
	if refused.status != http.StatusTooManyRequests {
		t.Fatalf("second download = %d, want 429", refused.status)
	}
	seconds, err := strconv.Atoi(refused.header.Get("Retry-After"))
	if err != nil || seconds <= 0 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", refused.header.Get("Retry-After"))
	}
	var refusal struct {
		Error     string `json:"error"`
		Code      string `json:"code"`
		Principal string `json:"principal"`
	}
	if err := json.Unmarshal(refused.body, &refusal); err != nil {
		t.Fatalf("decode refusal: %v -- raw %q", err, refused.body)
	}
	if refusal.Code != "egress_budget_exceeded" || refusal.Principal != BearerPrincipal {
		t.Fatalf("refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.Error, "egress budget exceeded") {
		t.Errorf("error member %q does not carry the reason", refusal.Error)
	}
}

func TestDownloadRoutesStillRequireTheBearer(t *testing.T) {
	srv := newBudgetedServer(t, "s3cret", egress.Config{})
	seedArtifact(t, "job1", "out.tar", 10)

	for _, path := range []string{
		artifactDownloadPath,
		"/archive?repo=x&branch=main",
		"/bin/anything",
		"/cache/anything",
		"/uploads/anything",
		"/file?repo=x&branch=main&path=p",
	} {
		if got := fetch(t, srv, path, "").status; got != http.StatusUnauthorized {
			t.Errorf("GET %s without a bearer = %d, want 401", path, got)
		}
	}
	if state := egressMeter.State(); state.GlobalMonthBytes != 0 {
		t.Errorf("an unauthenticated refusal metered %d bytes, want 0", state.GlobalMonthBytes)
	}

	// safety: the credentialed fetch a runner makes must keep working.
	got := fetch(t, srv, artifactDownloadPath, "s3cret")
	if got.status != http.StatusOK || len(got.body) == 0 {
		t.Fatalf("a bearer fetch = %d with %d bytes, want 200 and a body", got.status, len(got.body))
	}
}

func TestCacheHealthReportsTheEgressAlarm(t *testing.T) {
	srv := newBudgetedServer(t, "s3cret", egress.Config{GlobalDailyAlarmBytes: 1})
	seedArtifact(t, "job1", "out.tar", 100)

	var health struct {
		Status   string         `json:"status"`
		Problems []string       `json:"problems"`
		Egress   map[string]any `json:"egress"`
	}
	body := fetch(t, srv, "/health", "").body
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, body)
	}
	if health.Egress["alarm"] != false {
		t.Fatalf("health egress = %+v, want no alarm", health.Egress)
	}

	if got := fetch(t, srv, artifactDownloadPath, "s3cret").status; got != http.StatusOK {
		t.Fatalf("download = %d", got)
	}

	body = fetch(t, srv, "/health", "").body
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, body)
	}
	if health.Egress["alarm"] != true || health.Status != "degraded" {
		t.Fatalf("health = %+v, want a degraded status with the alarm up", health)
	}
	var named bool
	for _, p := range health.Problems {
		named = named || strings.Contains(p, "egress")
	}
	if !named {
		t.Errorf("health problems = %v, want one naming egress", health.Problems)
	}
}

func TestUnbudgetedCacheServesEveryDownload(t *testing.T) {
	srv := newBudgetedServer(t, "s3cret", egress.Config{})
	seedArtifact(t, "job1", "out.tar", 100)

	var served int
	for range 3 {
		got := fetch(t, srv, artifactDownloadPath, "s3cret")
		if got.status != http.StatusOK || len(got.body) == 0 {
			t.Fatalf("download = %d with %d bytes, want 200 and a body", got.status, len(got.body))
		}
		served += len(got.body)
	}
	if state := egressMeter.State(); state.GlobalMonthBytes != int64(served) {
		t.Fatalf("metered %d bytes, want the %d served without a budget", state.GlobalMonthBytes, served)
	}
}
