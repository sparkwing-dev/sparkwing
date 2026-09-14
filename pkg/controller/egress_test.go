package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func newEgressFixture(t *testing.T, cfg egress.Config, art storage.ArtifactStore) egressFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
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
	if art != nil {
		ctrl = ctrl.WithArtifactStore(art)
	}
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

func (f egressFixture) fetchAs(t *testing.T, path, token, pod string) fetched {
	t.Helper()
	req := f.request(t, http.MethodGet, path, token)
	req.Header.Set(store.RunnerIdentityHeader, pod)
	resp, err := http.DefaultClient.Do(req)
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

func (f egressFixture) spend(t *testing.T, path, token string) {
	t.Helper()
	f.get(t, path, token)
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

func TestArtifactDownloadCountsAgainstThePrincipalsBudget(t *testing.T) {
	art := &fakeArtifactStore{objects: map[string][]byte{"k": bytes.Repeat([]byte("x"), 100)}}
	f := newEgressFixture(t, egress.Config{PerPrincipalMonthlyBytes: 150}, art)

	first := f.get(t, "/api/v1/artifacts/k", f.adminToken)
	if first.status != http.StatusOK || len(first.body) != 100 {
		t.Fatalf("first download = %d with %d bytes, want 200 and 100", first.status, len(first.body))
	}
	if state := f.meter.State(); state.GlobalMonthBytes != 100 {
		t.Fatalf("metered %d bytes, want 100", state.GlobalMonthBytes)
	}

	if got := f.get(t, "/api/v1/artifacts/k", f.adminToken).status; got != http.StatusOK {
		t.Fatalf("second download = %d, want 200; the budget was not yet spent", got)
	}

	third := f.get(t, "/api/v1/artifacts/k", f.adminToken)
	if third.status != http.StatusTooManyRequests {
		t.Fatalf("third download = %d, want 429", third.status)
	}
	retry, err := strconv.Atoi(third.header.Get("Retry-After"))
	if err != nil || retry <= 0 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", third.header.Get("Retry-After"))
	}
	var refusal struct {
		Error      string `json:"error"`
		Code       string `json:"code"`
		Principal  string `json:"principal"`
		LimitBytes int64  `json:"limit_bytes"`
		UsedBytes  int64  `json:"used_bytes"`
	}
	if err := json.Unmarshal(third.body, &refusal); err != nil {
		t.Fatalf("decode refusal: %v -- raw %q", err, third.body)
	}
	if refusal.Code != controller.EgressBudgetCode || refusal.Principal != "root" {
		t.Fatalf("refusal = %+v", refusal)
	}
	if refusal.LimitBytes != 150 || refusal.UsedBytes != 200 {
		t.Fatalf("refusal counters = %+v, want 200 of 150", refusal)
	}
	if !strings.Contains(refusal.Error, "egress budget exceeded") {
		t.Errorf("error member %q does not carry the reason", refusal.Error)
	}
}

func TestOneBudgetDoesNotRefuseAnotherPrincipal(t *testing.T) {
	art := &fakeArtifactStore{objects: map[string][]byte{"k": bytes.Repeat([]byte("x"), 100)}}
	f := newEgressFixture(t, egress.Config{PerPrincipalMonthlyBytes: 50}, art)

	f.spend(t, "/api/v1/artifacts/k", f.adminToken)

	if got := f.get(t, "/api/v1/artifacts/k", f.adminToken).status; got != http.StatusTooManyRequests {
		t.Fatalf("the spent principal = %d, want 429", got)
	}

	// safety: the runner and CLI fetch paths must keep working while
	// another principal is over budget.
	other := f.get(t, "/api/v1/artifacts/k", f.reader)
	if other.status != http.StatusOK || len(other.body) != 100 {
		t.Fatalf("a CLI token fetching = %d with %d bytes, want 200 and 100", other.status, len(other.body))
	}
}

func TestMeteredRoutesStillServeTheRunnerAndCLITokens(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	art := &fakeArtifactStore{objects: map[string][]byte{"k": []byte("payload")}}
	f := newEgressFixture(t, egress.Config{PerPrincipalMonthlyBytes: 1 << 20, MaxStreamsPerPrincipal: 4, MaxDownloadsPerPrincipal: 4}, art)
	seedLiveLog(t, f, "r1", "n1", "hello\n")

	// safety: a runner reads a node's logs on its claim scope, which is the
	// path a metered route must not break.
	claimRunForRunner(t, f, "r1")
	runnerLogs := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.runner)
	if runnerLogs.status != http.StatusOK || !strings.Contains(string(runnerLogs.body), "hello") {
		t.Fatalf("runner log read = %d, body %q", runnerLogs.status, runnerLogs.body)
	}

	artifact := f.get(t, "/api/v1/artifacts/k", f.reader)
	if artifact.status != http.StatusOK || string(artifact.body) != "payload" {
		t.Fatalf("CLI artifact fetch = %d, body %q", artifact.status, artifact.body)
	}

	logs := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.reader)
	if logs.status != http.StatusOK || !strings.Contains(string(logs.body), "hello") {
		t.Fatalf("CLI log read = %d, body %q", logs.status, logs.body)
	}

	state := f.meter.State()
	if state.GlobalMonthBytes < int64(len(runnerLogs.body)+len(artifact.body)+len(logs.body)) {
		t.Errorf("metered %d bytes, want at least the three bodies", state.GlobalMonthBytes)
	}
	if len(state.Top) != 2 {
		t.Errorf("top consumers = %+v, want the runner and the CLI counted apart", state.Top)
	}
}

func TestDownloadRoutesRefuseARequestWithoutABearer(t *testing.T) {
	art := &fakeArtifactStore{objects: map[string][]byte{"k": []byte("payload")}}
	f := newEgressFixture(t, egress.Config{}, art)
	seedLiveLog(t, f, "r1", "n1", "hello\n")

	for _, path := range []string{
		"/api/v1/artifacts/k",
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
	art := &fakeArtifactStore{objects: map[string][]byte{"k": bytes.Repeat([]byte("x"), 1<<20)}}
	f := newEgressFixture(t, egress.Config{GlobalDailyAlarmBytes: 1 << 20, MaxDownloadsPerPrincipal: 1}, art)

	for range 8 {
		if got := f.fetch(t, http.MethodHead, "/api/v1/artifacts/k", f.adminToken).status; got != http.StatusOK {
			t.Fatalf("HEAD = %d, want 200", got)
		}
	}

	// safety: net/http discards a HEAD body, so eight of them over a
	// 1 MiB object once latched the daily alarm with nothing on the wire.
	state := f.meter.State()
	if state.GlobalDayBytes != 0 {
		t.Fatalf("eight HEADs charged %d bytes, want 0", state.GlobalDayBytes)
	}
	if state.Alarm {
		t.Fatal("a HEAD raised the daily alarm")
	}
}

// safety: this holds one Get open until released, which is how a test
// gets two downloads in flight against a concurrency cap of one.
type blockingArtifactStore struct {
	payload []byte
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

// safety: only the first Get blocks. A later one must answer, or a test
// that expects a second caller to be admitted waits on a store that never
// returns instead of on the slot it is measuring.
func (b *blockingArtifactStore) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	first := false
	b.once.Do(func() {
		first = true
		close(b.started)
	})
	if first {
		<-b.release
	}
	return io.NopCloser(bytes.NewReader(b.payload)), nil
}
func (b *blockingArtifactStore) Put(context.Context, string, io.Reader) error   { return nil }
func (b *blockingArtifactStore) Has(context.Context, string) (bool, error)      { return false, nil }
func (b *blockingArtifactStore) Delete(context.Context, string) error           { return nil }
func (b *blockingArtifactStore) List(context.Context, string) ([]string, error) { return nil, nil }

func TestConcurrentDownloadsAreCappedPerPrincipal(t *testing.T) {
	art := &blockingArtifactStore{
		payload: []byte("payload"),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	f := newEgressFixture(t, egress.Config{MaxDownloadsPerPrincipal: 1}, art)

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.spend(t, "/api/v1/artifacts/k", f.adminToken)
	}()
	<-art.started

	refused := f.get(t, "/api/v1/artifacts/k", f.adminToken)
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

	close(art.release)
	<-done

	// safety: the slot is released when the response ends, so the next
	// download is admitted.
	if got := f.get(t, "/api/v1/artifacts/k", f.adminToken).status; got != http.StatusOK {
		t.Fatalf("a download after the first finished = %d, want 200", got)
	}
}

func TestLiveLogStreamCapRefusesPastTheLimit(t *testing.T) {
	f := newEgressFixture(t, egress.Config{MaxStreamsPerPrincipal: 1}, nil)
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
	art := &fakeArtifactStore{objects: map[string][]byte{"k": bytes.Repeat([]byte("x"), 100)}}
	f := newEgressFixture(t, egress.Config{GlobalDailyAlarmBytes: 60}, art)

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

	f.spend(t, "/api/v1/artifacts/k", f.adminToken)

	after := f.get(t, "/api/v1/health", "")
	if err := json.Unmarshal(after.body, &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, after.body)
	}
	if health.Egress["alarm"] != true {
		t.Fatalf("health egress = %+v, want the alarm up", health.Egress)
	}
	if health.Status != "degraded" {
		t.Errorf("health status = %q, want degraded", health.Status)
	}
	var named bool
	for _, p := range health.Problems {
		named = named || strings.Contains(p, "egress")
	}
	if !named {
		t.Errorf("health problems = %v, want one naming egress", health.Problems)
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
	if len(top.Egress.Top) != 1 || top.Egress.Top[0].Principal != "root" || top.Egress.Top[0].MonthBytes != 100 {
		t.Fatalf("top consumers = %+v, want root at 100 bytes", top.Egress.Top)
	}
}

func TestTheTopConsumersViewIsAdminOnly(t *testing.T) {
	f := newEgressFixture(t, egress.Config{}, nil)
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

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
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

// safety: twenty pods share one pool bearer, so a cap keyed on the
// principal alone refuses nineteen of them at once.
func TestEachPodOfAPoolHoldsItsOwnDownloadSlot(t *testing.T) {
	art := &blockingArtifactStore{
		payload: []byte("payload"),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	f := newEgressFixture(t, egress.Config{MaxDownloadsPerPrincipal: 1}, art)

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := f.request(t, http.MethodGet, "/api/v1/artifacts/k", f.adminToken)
		req.Header.Set(store.RunnerIdentityHeader, "pod-a")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()
	<-art.started

	// safety: a second pod under the same bearer gets its own slot.
	other := f.fetchAs(t, "/api/v1/artifacts/k", f.adminToken, "pod-b")
	if other.status == http.StatusTooManyRequests {
		t.Fatal("a second pod was refused by the first pod's slot")
	}

	// safety: the cap still holds within one pod.
	same := f.fetchAs(t, "/api/v1/artifacts/k", f.adminToken, "pod-a")
	if same.status != http.StatusTooManyRequests {
		t.Fatalf("the same pod's second simultaneous download = %d, want 429", same.status)
	}

	close(art.release)
	<-done
}
