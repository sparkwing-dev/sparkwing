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

func (f egressFixture) get(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// safety: the helper closes what it opened, so a caller that only needs
// the bytes gone does not leak the connection behind them.
func (f egressFixture) spend(t *testing.T, path, token string) {
	t.Helper()
	resp := f.get(t, path, token)
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain response: %v", err)
	}
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

	resp := f.get(t, "/api/v1/artifacts/k", f.adminToken)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) != 100 {
		t.Fatalf("first download = %d with %d bytes, want 200 and 100", resp.StatusCode, len(body))
	}
	if state := f.meter.State(); state.GlobalMonthBytes != 100 {
		t.Fatalf("metered %d bytes, want 100", state.GlobalMonthBytes)
	}

	second := f.get(t, "/api/v1/artifacts/k", f.adminToken)
	secondStatus := second.StatusCode
	if _, err := io.Copy(io.Discard, second.Body); err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if secondStatus != http.StatusOK {
		t.Fatalf("second download = %d, want 200; the budget was not yet spent", secondStatus)
	}

	third := f.get(t, "/api/v1/artifacts/k", f.adminToken)
	defer third.Body.Close()
	if third.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third download = %d, want 429", third.StatusCode)
	}
	retry, err := strconv.Atoi(third.Header.Get("Retry-After"))
	if err != nil || retry <= 0 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", third.Header.Get("Retry-After"))
	}
	var refusal struct {
		Error      string `json:"error"`
		Code       string `json:"code"`
		Principal  string `json:"principal"`
		LimitBytes int64  `json:"limit_bytes"`
		UsedBytes  int64  `json:"used_bytes"`
	}
	if err := json.NewDecoder(third.Body).Decode(&refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
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

	refused := f.get(t, "/api/v1/artifacts/k", f.adminToken)
	refused.Body.Close()
	if refused.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the spent principal = %d, want 429", refused.StatusCode)
	}

	// safety: the runner and CLI fetch paths must keep working while
	// another principal is over budget.
	other := f.get(t, "/api/v1/artifacts/k", f.reader)
	body, _ := io.ReadAll(other.Body)
	other.Body.Close()
	if other.StatusCode != http.StatusOK || len(body) != 100 {
		t.Fatalf("a runner-scoped token fetching = %d with %d bytes, want 200 and 100", other.StatusCode, len(body))
	}
}

func TestMeteredRoutesStillServeTheRunnerAndCLITokens(t *testing.T) {
	art := &fakeArtifactStore{objects: map[string][]byte{"k": []byte("payload")}}
	f := newEgressFixture(t, egress.Config{PerPrincipalMonthlyBytes: 1 << 20, MaxStreamsPerPrincipal: 4}, art)
	seedLiveLog(t, f, "r1", "n1", "hello\n")

	// safety: a runner reads a node's logs on its claim scope, which is the
	// path a metered route must not break.
	claimRunForRunner(t, f, "r1")
	runnerLogs := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.runner)
	runnerBody, err := io.ReadAll(runnerLogs.Body)
	runnerLogs.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if runnerLogs.StatusCode != http.StatusOK || !strings.Contains(string(runnerBody), "hello") {
		t.Fatalf("runner log read = %d, body %q", runnerLogs.StatusCode, runnerBody)
	}

	artifact := f.get(t, "/api/v1/artifacts/k", f.reader)
	body, err := io.ReadAll(artifact.Body)
	artifact.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if artifact.StatusCode != http.StatusOK || string(body) != "payload" {
		t.Fatalf("CLI artifact fetch = %d, body %q", artifact.StatusCode, body)
	}

	logs := f.get(t, "/api/v1/runs/r1/nodes/n1/logs", f.reader)
	logBody, err := io.ReadAll(logs.Body)
	logs.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if logs.StatusCode != http.StatusOK || !strings.Contains(string(logBody), "hello") {
		t.Fatalf("CLI log read = %d, body %q", logs.StatusCode, logBody)
	}

	state := f.meter.State()
	if state.GlobalMonthBytes < int64(len(runnerBody)+len(body)+len(logBody)) {
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
		resp := f.get(t, path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a bearer = %d, want 401", path, resp.StatusCode)
		}
	}
	if state := f.meter.State(); state.GlobalMonthBytes != 0 {
		t.Errorf("an unauthenticated refusal metered %d bytes, want 0", state.GlobalMonthBytes)
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
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second stream = %d, want 429", second.StatusCode)
	}
	if second.Header.Get("Retry-After") == "" {
		t.Error("the stream refusal named no Retry-After")
	}
	var refusal struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(second.Body).Decode(&refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if refusal.Code != controller.EgressStreamLimitCode {
		t.Fatalf("refusal code = %q, want %q", refusal.Code, controller.EgressStreamLimitCode)
	}
	if !strings.Contains(refusal.Error, "live log stream limit reached") {
		t.Errorf("error member %q does not carry the reason", refusal.Error)
	}
}

func TestHealthAndTheTopConsumersViewReportTheAlarm(t *testing.T) {
	art := &fakeArtifactStore{objects: map[string][]byte{"k": bytes.Repeat([]byte("x"), 100)}}
	f := newEgressFixture(t, egress.Config{GlobalDailyAlarmBytes: 60}, art)

	before := f.get(t, "/api/v1/health", "")
	var health struct {
		Status   string         `json:"status"`
		Problems []string       `json:"problems"`
		Egress   map[string]any `json:"egress"`
	}
	if err := json.NewDecoder(before.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	before.Body.Close()
	if health.Egress["alarm"] != false {
		t.Fatalf("health egress = %+v, want no alarm", health.Egress)
	}

	f.spend(t, "/api/v1/artifacts/k", f.adminToken)

	after := f.get(t, "/api/v1/health", "")
	if err := json.NewDecoder(after.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	after.Body.Close()
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
	defer view.Body.Close()
	if view.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/egress = %d, want 200", view.StatusCode)
	}
	var top struct {
		Enabled bool         `json:"enabled"`
		Egress  egress.State `json:"egress"`
	}
	if err := json.NewDecoder(view.Body).Decode(&top); err != nil {
		t.Fatalf("decode the top-consumers view: %v", err)
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
	resp := f.get(t, "/api/v1/egress", f.runner)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a runner reading the egress view = %d, want 403", resp.StatusCode)
	}
}
