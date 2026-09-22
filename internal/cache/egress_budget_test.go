package cache

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func newBudgetedServer(t *testing.T, token string, cfg egress.Config) *httptest.Server {
	t.Helper()
	savedMeter := egressMeter
	egressMeter = nil
	t.Cleanup(func() { egressMeter = savedMeter })

	saved := struct {
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir string
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken        string
		teamsDir                                                    string
	}{
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir,
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken,
		teamsDir,
	}
	t.Cleanup(func() {
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir = saved.dataRoot, saved.repoDir, saved.archDir, saved.artifactsDir, saved.binsDir, saved.cacheDir
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken = saved.uploadsDir, saved.namesFile, saved.proxyDir, saved.sshKeyDir, saved.apiToken
		teamsDir = saved.teamsDir
	})

	root := t.TempDir()
	c := DefaultConfig()
	c.DataDir = root
	c.ProxyDir = filepath.Join(root, "proxy")
	c.SSHKeyDir = filepath.Join(root, "no-ssh-key")
	c.APIToken = token
	c.AllowUnauthenticated = token == ""
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

func fetch(t *testing.T, srv *httptest.Server, method, path, token string) fetched {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
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

func get(t *testing.T, srv *httptest.Server, path, token string) fetched {
	t.Helper()
	return fetch(t, srv, http.MethodGet, path, token)
}

const artifactDownloadPath = "/artifacts/job1?glob=*"

// safety: the cache resolves every credentialed caller to one name, so a
// per-principal refusal here would stop every runner's checkout and
// cache read at once, for up to a month, with no recovery but a restart.
// It meters and alarms; the refusal lives on the controller and the logs
// service, where the principal is real.
func TestTheCacheNeverRefusesADownload(t *testing.T) {
	srv := newBudgetedServer(t, "s3cret", egress.Config{GlobalDailyAlarmBytes: 1})
	seedArtifact(t, "job1", "out.tar", 100)

	var served int
	for i := range 12 {
		got := get(t, srv, artifactDownloadPath, "s3cret")
		if got.status != http.StatusOK || len(got.body) == 0 {
			t.Fatalf("download %d = %d with %d bytes, want 200 and a body", i, got.status, len(got.body))
		}
		if got.header.Get("Retry-After") != "" {
			t.Fatalf("the cache named a Retry-After on download %d", i)
		}
		served += len(got.body)
	}
	if !egressMeter.Alarm() {
		t.Fatal("twelve downloads past the threshold raised no alarm")
	}
	state := egressMeter.State()
	if state.GlobalMonthBytes != int64(served) {
		t.Fatalf("metered %d bytes, want the %d served", state.GlobalMonthBytes, served)
	}
	if state.Refused != 0 {
		t.Fatalf("the cache refused %d requests, want none", state.Refused)
	}
}

// safety: the cache cannot enforce a per-principal budget, so it must not
// hold one; a configuration that carries one is a cap nobody applies.
func TestTheCacheCarriesNoPerPrincipalBudget(t *testing.T) {
	newBudgetedServer(t, "s3cret", egress.Config{GlobalDailyAlarmBytes: 1})
	cfg := egressMeter.Config()
	if cfg.PerPrincipalMonthlyBytes != 0 || cfg.MaxDownloadsPerPrincipal != 0 || cfg.MaxStreamsPerPrincipal != 0 {
		t.Fatalf("cache meter config = %+v, want only the daily alarm", cfg)
	}
	state, _ := egressHealth()
	if state["enforced"] != false {
		t.Fatalf("health = %+v, want enforced false", state)
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
		if got := get(t, srv, path, "").status; got != http.StatusUnauthorized {
			t.Errorf("GET %s without a bearer = %d, want 401", path, got)
		}
	}
	if state := egressMeter.State(); state.GlobalMonthBytes != 0 {
		t.Errorf("an unauthenticated refusal metered %d bytes, want 0", state.GlobalMonthBytes)
	}

	// safety: the credentialed fetch a runner makes must keep working.
	got := get(t, srv, artifactDownloadPath, "s3cret")
	if got.status != http.StatusOK || len(got.body) == 0 {
		t.Fatalf("a bearer fetch = %d with %d bytes, want 200 and a body", got.status, len(got.body))
	}
}

// safety: net/http discards a HEAD body and an error body is not the
// download the alarm is for, so neither reaches the day's total; charging
// a HEAD once let eight of them latch the alarm with nothing on the wire.
func TestBodylessAndRefusedResponsesChargeNothing(t *testing.T) {
	srv := newBudgetedServer(t, "s3cret", egress.Config{GlobalDailyAlarmBytes: 1})
	seedArtifact(t, "job1", "out.tar", 1<<16)

	for range 8 {
		if got := fetch(t, srv, http.MethodHead, artifactDownloadPath, "s3cret"); got.status < 400 {
			t.Fatalf("HEAD = %d, want the route to answer it without a body", got.status)
		}
		if got := get(t, srv, "/artifacts/job1?glob=nothing-matches-this", "s3cret"); got.status >= 500 {
			t.Fatalf("an empty glob = %d", got.status)
		}
		if got := get(t, srv, artifactDownloadPath, ""); got.status != http.StatusUnauthorized {
			t.Fatalf("an unauthenticated download = %d, want 401", got.status)
		}
	}
	if state := egressMeter.State(); state.GlobalDayBytes != 0 {
		t.Fatalf("bodyless and refused responses charged %d bytes, want 0", state.GlobalDayBytes)
	}
	if egressMeter.Alarm() {
		t.Fatal("a bodyless or refused response raised the daily alarm")
	}
}

// safety: a second New with the same budget must not restart the day's
// total behind an alarm that is already up.
func TestASecondServerKeepsAnUnchangedMetersCounters(t *testing.T) {
	newBudgetedServer(t, "s3cret", egress.Config{GlobalDailyAlarmBytes: 1 << 30})
	egressMeter.Record(BearerPrincipal, egress.ClassArtifact, 500)
	first := egressMeter

	setEgressMeter(egress.Config{GlobalDailyAlarmBytes: 1 << 30})
	if egressMeter != first {
		t.Fatal("an unchanged budget replaced the live meter")
	}
	if got := egressMeter.State().GlobalDayBytes; got != 500 {
		t.Fatalf("day bytes after a second New = %d, want 500", got)
	}

	setEgressMeter(egress.Config{GlobalDailyAlarmBytes: 1 << 20})
	if egressMeter == first {
		t.Fatal("a changed budget kept the old meter")
	}
	if got := egressMeter.State().GlobalDayBytes; got != 0 {
		t.Fatalf("day bytes after a changed budget = %d, want a fresh meter", got)
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
	body := get(t, srv, "/health", "").body
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, body)
	}
	if health.Egress["alarm"] != false {
		t.Fatalf("health egress = %+v, want no alarm", health.Egress)
	}

	if got := get(t, srv, artifactDownloadPath, "s3cret").status; got != http.StatusOK {
		t.Fatalf("download = %d", got)
	}

	body = get(t, srv, "/health", "").body
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, body)
	}
	if health.Egress["alarm"] != true || health.Status != "degraded" {
		t.Fatalf("health = %+v, want a degraded status with the alarm up", health)
	}
	if health.Egress["enforced"] != false {
		t.Fatalf("health egress = %+v, want enforced false", health.Egress)
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
		got := get(t, srv, artifactDownloadPath, "s3cret")
		if got.status != http.StatusOK || len(got.body) == 0 {
			t.Fatalf("download = %d with %d bytes, want 200 and a body", got.status, len(got.body))
		}
		served += len(got.body)
	}
	if state := egressMeter.State(); state.GlobalMonthBytes != int64(served) {
		t.Fatalf("metered %d bytes, want the %d served without a budget", state.GlobalMonthBytes, served)
	}
}
