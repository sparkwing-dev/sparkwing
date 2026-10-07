package controller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type egressFixture struct {
	url        string
	store      *store.Store
	meter      *egress.Meter
	adminToken string
	runner     string
	runnerID   store.ClaimIdentity
	reader     string
}

func newEgressFixture(t *testing.T, cfg egress.Config) egressFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	runner, runnerTok, err := st.CreateToken("pool", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeTriggersClaim,
		controller.ScopeRunsState, controller.ScopeLogsWrite,
	}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken runner: %v", err)
	}
	reader, _, err := st.CreateToken("cli", store.TokenKindUser, []string{
		controller.ScopeRunsRead, controller.ScopeLogsRead,
	}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken reader: %v", err)
	}

	meter := egress.New(cfg)
	ctrl := controller.New(st, nil).EnableAuthFromStore().WithEgressMeter(meter)
	srv := httptest.NewServer(ctrl.Handler())
	t.Cleanup(srv.Close)
	return egressFixture{
		url: srv.URL, store: st, meter: meter,
		adminToken: admin, reader: reader,
		runner:   runner,
		runnerID: store.ClaimIdentity{Principal: "pool", TokenPrefix: runnerTok.Prefix},
	}
}

// safety: helpers hand back a whole answer rather than a live
// *http.Response, so no call site owns a body it can forget to close.
type fetched struct {
	status int
	header http.Header
	body   []byte
}

func (f egressFixture) request(t *testing.T, method, path, token string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func (f egressFixture) fetch(t *testing.T, method, path, token string) fetched {
	t.Helper()
	resp, err := http.DefaultClient.Do(f.request(t, method, path, token))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return fetched{status: resp.StatusCode, header: resp.Header, body: body}
}

func (f egressFixture) get(t *testing.T, path, token string) fetched {
	t.Helper()
	return f.fetch(t, http.MethodGet, path, token)
}

func seedLiveLog(t *testing.T, f egressFixture, runID, nodeID, text string) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		f.url+"/api/v1/runs/"+runID+"/nodes/"+nodeID+"/logs", strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("append live log: status %d", resp.StatusCode)
	}
}

func claimRunForRunner(t *testing.T, f egressFixture, runID string) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: "p", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	claimed, err := f.store.ClaimNextTriggerFor(ctx, f.runnerID, time.Minute, nil, nil)
	if err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	if claimed == nil || claimed.ID != runID {
		t.Fatalf("claimed trigger = %+v, want %s", claimed, runID)
	}
}

func TestMeteredRoutesStillServeTheRunnerAndCLITokens(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newEgressFixture(t, egress.Config{MaxStreamsPerPrincipal: 4, MaxDownloadsPerPrincipal: 4})
	seedLiveLog(t, f, "r1", "n1", "hello\n")

	// safety: a runner reads a node's logs on its claim scope, which is the
	// path a metered route must not break.
	claimRunForRunner(t, f, "r1")
	runnerLogs := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.runner)
	if runnerLogs.status != http.StatusOK || !strings.Contains(string(runnerLogs.body), "hello") {
		t.Fatalf("runner log read = %d, body %q", runnerLogs.status, runnerLogs.body)
	}

	logs := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.reader)
	if logs.status != http.StatusOK || !strings.Contains(string(logs.body), "hello") {
		t.Fatalf("CLI log read = %d, body %q", logs.status, logs.body)
	}

	state := f.meter.State()
	if state.GlobalMonthBytes < int64(len(runnerLogs.body)+len(logs.body)) {
		t.Errorf("metered %d bytes, want at least the two bodies", state.GlobalMonthBytes)
	}
	if len(state.Top) != 2 {
		t.Errorf("top consumers = %+v, want the runner and the CLI counted apart", state.Top)
	}
}

func TestDownloadRoutesRefuseARequestWithoutABearer(t *testing.T) {
	f := newEgressFixture(t, egress.Config{})
	seedLiveLog(t, f, "r1", "n1", "hello\n")

	for _, path := range []string{
		"/api/v1/runs/r1/nodes/n1/logs",
		"/api/v1/runs/r1/nodes/n1/logs/stream",
	} {
		if got := f.get(t, path, "").status; got != http.StatusUnauthorized {
			t.Errorf("GET %s without a bearer = %d, want 401", path, got)
		}
	}
	if state := f.meter.State(); state.GlobalMonthBytes != 0 {
		t.Errorf("an unauthenticated refusal metered %d bytes, want 0", state.GlobalMonthBytes)
	}
}

func TestHeadRequestsChargeNothingAndHoldNoSlot(t *testing.T) {
	f := newEgressFixture(t, egress.Config{GlobalDailyAlarmBytes: 4 << 10, MaxDownloadsPerPrincipal: 1})
	seedLiveLog(t, f, "r1", "n1", strings.Repeat("x", 1<<10)+"\n")

	for range 8 {
		if got := f.fetch(t, http.MethodHead, "/api/v1/runs/r1/nodes/n1/logs", f.adminToken).status; got != http.StatusOK {
			t.Fatalf("HEAD = %d, want 200", got)
		}
	}

	// safety: net/http discards a HEAD body, so eight of them over a
	// 1 KiB log once latched a 4 KiB daily alarm with nothing on the wire.
	state := f.meter.State()
	if state.GlobalDayBytes != 0 {
		t.Fatalf("eight HEADs charged %d bytes, want 0", state.GlobalDayBytes)
	}
	if state.Alarm {
		t.Fatal("a HEAD raised the daily alarm")
	}
}

func TestConcurrentDownloadsAreCappedPerPrincipal(t *testing.T) {
	f := newEgressFixture(t, egress.Config{MaxDownloadsPerPrincipal: 1})
	seedLiveLog(t, f, "r1", "n1", "hello\n")

	// safety: a log read finishes before a second request can overlap it, so
	// the first download's slot is taken on the meter directly.
	release, err := f.meter.Open(egress.TeamPrincipal(string(store.DefaultTeam), "root"), egress.SlotDownload)
	if err != nil {
		t.Fatalf("hold the principal's download slot: %v", err)
	}
	refused := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.adminToken)
	if refused.status != http.StatusTooManyRequests {
		t.Fatalf("a second simultaneous download = %d, want 429", refused.status)
	}
	var refusal struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(refused.body, &refusal); err != nil {
		t.Fatalf("decode refusal: %v -- raw %q", err, refused.body)
	}
	if refusal.Code != controller.EgressDownloadLimitCode {
		t.Fatalf("refusal code = %q, want %q", refusal.Code, controller.EgressDownloadLimitCode)
	}
	if !strings.Contains(refusal.Error, "egress concurrency limit reached") {
		t.Errorf("error member %q does not carry the reason", refusal.Error)
	}

	release()
	if got := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.adminToken).status; got != http.StatusOK {
		t.Fatalf("a download after the first finished = %d, want 200", got)
	}
	if got := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.adminToken).status; got != http.StatusOK {
		t.Fatalf("a download after the second finished = %d, want 200; the route kept its slot", got)
	}
}

func TestLiveLogStreamCapRefusesPastTheLimit(t *testing.T) {
	f := newEgressFixture(t, egress.Config{MaxStreamsPerPrincipal: 1})
	seedLiveLog(t, f, "r1", "n1", "hello\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		f.url+"/api/v1/runs/r1/nodes/n1/logs/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.adminToken)
	open, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Body.Close()
	if open.StatusCode != http.StatusOK {
		t.Fatalf("first stream = %d, want 200", open.StatusCode)
	}
	if got := open.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("live log stream Cache-Control = %q, want no-store", got)
	}
	// safety: the handler holds the slot only once it has written, so the
	// first byte of the stream is what proves the reservation is in place.
	buf := make([]byte, 1)
	if _, err := open.Body.Read(buf); err != nil {
		t.Fatalf("read the open stream: %v", err)
	}

	second := f.get(t, "/api/v1/runs/r1/nodes/n1/logs/stream", f.adminToken)
	if second.status != http.StatusTooManyRequests {
		t.Fatalf("second stream = %d, want 429", second.status)
	}
	if second.header.Get("Retry-After") == "" {
		t.Error("the stream refusal named no Retry-After")
	}
	var refusal struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(second.body, &refusal); err != nil {
		t.Fatalf("decode refusal: %v -- raw %q", err, second.body)
	}
	if refusal.Code != controller.EgressStreamLimitCode {
		t.Fatalf("refusal code = %q, want %q", refusal.Code, controller.EgressStreamLimitCode)
	}
	if !strings.Contains(refusal.Error, "egress concurrency limit reached") {
		t.Errorf("error member %q does not carry the reason", refusal.Error)
	}
}

func TestHealthAndTheTopConsumersViewReportTheAlarm(t *testing.T) {
	f := newEgressFixture(t, egress.Config{GlobalDailyAlarmBytes: 60})
	seedLiveLog(t, f, "r1", "n1", strings.Repeat("x", 100)+"\n")
	before0 := f.meter.State().GlobalMonthBytes

	var health struct {
		Status   string         `json:"status"`
		Problems []string       `json:"problems"`
		Egress   map[string]any `json:"egress"`
	}
	before := f.get(t, "/api/v1/health", "")
	if err := json.Unmarshal(before.body, &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, before.body)
	}
	if health.Egress["alarm"] != false {
		t.Fatalf("health egress = %+v, want no alarm", health.Egress)
	}

	read := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.adminToken)
	if read.status != http.StatusOK {
		t.Fatalf("log read = %d, want 200", read.status)
	}

	after := f.get(t, "/api/v1/health", "")
	if err := json.Unmarshal(after.body, &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, after.body)
	}
	if health.Egress["alarm"] != true {
		t.Fatalf("health egress = %+v, want the alarm up", health.Egress)
	}
	if len(health.Egress) != 2 || health.Egress["enabled"] != true {
		t.Fatalf("public health exposed egress usage: %+v", health.Egress)
	}
	if health.Status != "degraded" {
		t.Errorf("health status = %q, want degraded", health.Status)
	}
	if len(health.Problems) != 1 || health.Problems[0] != "egress: daily alarm threshold reached" {
		t.Errorf("public health exposed egress usage in problems: %v", health.Problems)
	}

	view := f.get(t, "/api/v1/egress", f.adminToken)
	if view.status != http.StatusOK {
		t.Fatalf("GET /api/v1/egress = %d, want 200", view.status)
	}
	var top struct {
		Enabled bool         `json:"enabled"`
		Egress  egress.State `json:"egress"`
	}
	if err := json.Unmarshal(view.body, &top); err != nil {
		t.Fatalf("decode the top-consumers view: %v -- raw %q", err, view.body)
	}
	if !top.Enabled || !top.Egress.Alarm {
		t.Fatalf("view = %+v, want an enabled meter with the alarm up", top)
	}
	if len(top.Egress.Top) != 1 || top.Egress.Top[0].Principal != "default/root" || top.Egress.Top[0].MonthBytes != before0+int64(len(read.body)) {
		t.Fatalf("top consumers = %+v, want default/root at the %d bytes it read", top.Egress.Top, len(read.body))
	}
}

func TestTheTopConsumersViewIsAdminOnly(t *testing.T) {
	f := newEgressFixture(t, egress.Config{})
	if got := f.get(t, "/api/v1/egress", f.runner).status; got != http.StatusForbidden {
		t.Fatalf("a runner reading the egress view = %d, want 403", got)
	}
}

// safety: the gitcache proxy is the checkout path every node takes and a
// whole pool shares one bearer, so a concurrency cap there refuses the
// clone rather than the download it was meant to bound. It stays
// byte-metered.
func TestTheGitcacheProxyIsByteMeteredButHoldsNoSlot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("packfile-bytes"))
	}))
	t.Cleanup(upstream.Close)

	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	meter := egress.New(egress.Config{MaxDownloadsPerPrincipal: 1})
	srv := httptest.NewServer(controller.New(st, nil).
		EnableAuthFromStore().WithCacheURL(upstream.URL).WithEgressMeter(meter).Handler())
	t.Cleanup(srv.Close)
	f := egressFixture{url: srv.URL, store: st, meter: meter, adminToken: admin}

	// safety: the cap of one would refuse the second of these if the proxy
	// took a slot, even though neither overlaps the other.
	for i := range 4 {
		got := f.get(t, "/api/v1/gitcache/git/widgets/info/refs?service=git-upload-pack", f.adminToken)
		if got.status != http.StatusOK {
			t.Fatalf("gitcache fetch %d = %d, body %q", i, got.status, got.body)
		}
	}
	if state := meter.State(); state.GlobalMonthBytes == 0 {
		t.Fatal("the gitcache proxy metered no bytes")
	}
	if state := meter.State(); state.Refused != 0 {
		t.Fatalf("the gitcache proxy refused %d requests, want none", state.Refused)
	}
}

// safety: the pod header is the caller's to write, so a slot keyed on it
// let one bearer open as many downloads as it invented pod names.
func TestARequestNamingAnotherPodSharesThePrincipalsDownloadSlot(t *testing.T) {
	f := newEgressFixture(t, egress.Config{MaxDownloadsPerPrincipal: 1})
	seedLiveLog(t, f, "r1", "n1", "hello\n")
	release, err := f.meter.Open(egress.TeamPrincipal(string(store.DefaultTeam), "root"), egress.SlotDownload)
	if err != nil {
		t.Fatalf("hold the principal's download slot: %v", err)
	}
	defer release()

	for _, header := range []string{store.RunnerIdentityHeader, store.ClaimHolderHeader} {
		req := f.request(t, http.MethodGet, "/api/v1/runs/r1/nodes/n1/logs", f.adminToken)
		req.Header.Set(header, "pod-b")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("a second download naming %s pod-b = %d, want 429 from the principal's one slot", header, resp.StatusCode)
		}
	}
}
