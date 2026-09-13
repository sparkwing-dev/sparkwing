package controller_test

import (
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

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type storageFixture struct {
	url    string
	store  *store.Store
	admin  string
	team   string
	other  string
	fence  store.NodeClaimFence
	server *controller.Server
}

// safety: a charged write needs a live claim, so the team token claims the
// node here and the second token deliberately holds nothing.
func newStorageFixture(t *testing.T, quotas ...store.StorageQuota) storageFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	runnerScopes := []string{
		controller.ScopeNodesClaim, controller.ScopeRunsState,
		controller.ScopeRunsRead, controller.ScopeLogsWrite,
	}
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	team, _, err := st.CreateToken("acme", store.TokenKindRunner, runnerScopes, 0, now)
	if err != nil {
		t.Fatalf("team token: %v", err)
	}
	other, _, err := st.CreateToken("other", store.TokenKindRunner, runnerScopes, 0, now)
	if err != nil {
		t.Fatalf("other token: %v", err)
	}
	for _, q := range quotas {
		if err := st.SetStorageQuota(ctx, q); err != nil {
			t.Fatalf("set quota for %s: %v", q.Principal, err)
		}
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "p", Status: "running", StartedAt: now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "only", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "only"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	srv := controller.New(st, nil).EnableAuthFromStore()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	n, err := client.NewWithToken(ts.URL, nil, team).ClaimNode(ctx, "runner:acme:1", nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("claim node: %v", err)
	}
	return storageFixture{
		url: ts.URL, store: st, admin: admin, team: team, other: other, server: srv,
		fence: store.NodeClaimFence{
			HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
			ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		},
	}
}

func (f storageFixture) request(t *testing.T, method, path, token, body string, fenced bool) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.url+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if fenced {
		req.Header.Set(store.ClaimHolderHeader, f.fence.HolderID)
		req.Header.Set(store.ClaimMembershipHeader, f.fence.MembershipID)
		req.Header.Set(store.ClaimReservationHeader, f.fence.ReservationID)
		req.Header.Set(store.ClaimGenerationHeader, strconv.FormatInt(f.fence.ClaimGeneration, 10))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func (f storageFixture) monthUsage(t *testing.T, principal string) store.StorageUsage {
	t.Helper()
	usage, err := f.store.StorageUsageFor(context.Background(), principal, "run-1",
		store.StorageMonth(time.Now().UTC()))
	if err != nil {
		t.Fatalf("usage for %s: %v", principal, err)
	}
	return usage
}

const (
	storageEventPath    = "/api/v1/runs/run-1/events"
	storageManifestPath = "/api/v1/runs/run-1/nodes/only/artifact-manifest"
)

func eventBody(payload string) string {
	buf, err := json.Marshal(map[string]any{
		"kind": "note", "node_id": "only", "payload": []byte(payload),
	})
	if err != nil {
		panic(err)
	}
	return string(buf)
}

func TestEventAppendRefusedPastTheBytesPerRunQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 8})
	if code, body := f.request(t, http.MethodPost, storageEventPath, f.team, eventBody("12345678"), true); code != http.StatusOK {
		t.Fatalf("an event inside the quota = %d %s, want 200", code, body)
	}
	code, body := f.request(t, http.MethodPost, storageEventPath, f.team, eventBody("x"), true)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the event past the quota = %d %s, want 413", code, body)
	}
	if !strings.Contains(body, store.StorageLimitBytesPerRun) || !strings.Contains(body, "acme") {
		t.Fatalf("the refusal reads %q and names neither the limit nor the team", body)
	}
	if got := f.monthUsage(t, "acme").RunBytes; got != 8 {
		t.Fatalf("charged bytes = %d, want 8; the refused event was charged", got)
	}
}

func TestEventAppendRefusedPastTheBytesPerMonthQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerMonth: 4})
	code, body := f.request(t, http.MethodPost, storageEventPath, f.team, eventBody("12345"), true)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the event past the month quota = %d %s, want 413", code, body)
	}
	if !strings.Contains(body, store.StorageLimitBytesPerMonth) {
		t.Fatalf("the refusal reads %q and does not name the monthly limit", body)
	}
}

func TestArtifactManifestRefusedPastTheObjectsPerRunQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxObjectsPerRun: 1})
	manifest := `{"manifest_digest":"sha256:abc"}`
	if code, body := f.request(t, http.MethodPost, storageManifestPath, f.team, manifest, true); code != http.StatusNoContent {
		t.Fatalf("the first manifest = %d %s, want 204", code, body)
	}
	code, body := f.request(t, http.MethodPost, storageManifestPath, f.team, manifest, true)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the second manifest = %d %s, want 413", code, body)
	}
	if !strings.Contains(body, store.StorageLimitObjectsPerRun) {
		t.Fatalf("the refusal reads %q and does not name the object limit", body)
	}
}

func TestStorageChargesTheCallersOwnTeamAndNoOther(t *testing.T) {
	f := newStorageFixture(t,
		store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1024},
		store.StorageQuota{Principal: "other", MaxBytesPerRun: 1024})
	if code, body := f.request(t, http.MethodPost, storageEventPath, f.team, eventBody("12345678"), true); code != http.StatusOK {
		t.Fatalf("the team's own event = %d %s, want 200", code, body)
	}
	if got := f.monthUsage(t, "acme").RunBytes; got != 8 {
		t.Fatalf("acme was charged %d bytes, want 8", got)
	}
	if got := f.monthUsage(t, "other").RunBytes; got != 0 {
		t.Fatalf("the other team was charged %d bytes for a write it did not make", got)
	}

	if code, body := f.request(t, http.MethodPost, storageEventPath, f.other,
		eventBody("12345678"), false); code != http.StatusForbidden {
		t.Fatalf("a token presenting no claim = %d %s, want 403", code, body)
	}
	code, body := f.request(t, http.MethodPost, storageEventPath, f.other, eventBody("12345678"), true)
	if code != http.StatusConflict {
		t.Fatalf("a token replaying another team's claim fence = %d %s, want 409", code, body)
	}
	if got := f.monthUsage(t, "other").RunBytes; got != 0 {
		t.Fatalf("a refused write charged the other team %d bytes", got)
	}
	if got := f.monthUsage(t, "acme").RunBytes; got != 8 {
		t.Fatalf("another team's refused write moved acme to %d bytes, want 8", got)
	}
}

func TestLiveLogAppendIsNotChargedToStorage(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 8})
	path := "/api/v1/runs/run-1/nodes/only/logs"
	for range 3 {
		if code, body := f.request(t, http.MethodPost, path, f.team,
			strings.Repeat("x", 4096), true); code != http.StatusNoContent {
			t.Fatalf("a live-log append = %d %s, want 204; the in-memory ring is not durable storage", code, body)
		}
	}
	if got := f.monthUsage(t, "acme"); got.RunBytes != 0 || got.MonthBytes != 0 {
		t.Fatalf("live logs charged %+v, want nothing", got)
	}
}

func TestStorageWritesUnboundedWithoutAQuota(t *testing.T) {
	f := newStorageFixture(t)
	for range 3 {
		if code, body := f.request(t, http.MethodPost, storageEventPath, f.team,
			eventBody(strings.Repeat("x", 2048)), true); code != http.StatusOK {
			t.Fatalf("an unquotaed event = %d %s, want 200", code, body)
		}
	}
	if got := f.monthUsage(t, "acme"); got.RunBytes != 0 {
		t.Fatalf("usage = %+v, want nothing counted while quotas are off", got)
	}
}

func TestStorageSettingsAndQuotaRoutes(t *testing.T) {
	f := newStorageFixture(t)
	code, body := f.request(t, http.MethodPut, "/api/v1/storage/settings", f.admin,
		`{"event_retention_days":30,"node_metric_retention_days":14,`+
			`"database_alarm_bytes":1073741824,"default_tier":"free"}`, false)
	if code != http.StatusOK {
		t.Fatalf("settings write = %d %s, want 200", code, body)
	}
	settings, err := f.store.StorageSettings(context.Background())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if !settings.RetentionOn() || settings.DefaultTier != store.StorageTierFree {
		t.Fatalf("stored settings = %+v, want retention on and the free tier", settings)
	}

	if code, body := f.request(t, http.MethodPut, "/api/v1/storage/quotas/acme", f.admin,
		`{"tier":"paid"}`, false); code != http.StatusOK {
		t.Fatalf("quota write = %d %s, want 200", code, body)
	}
	quota, err := f.store.StorageQuotaFor(context.Background(), "acme")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.MaxBytesPerRun != store.PaidTierQuota.MaxBytesPerRun {
		t.Fatalf("quota = %+v, want the paid tier limits", quota)
	}

	if code, body := f.request(t, http.MethodPut, "/api/v1/storage/quotas/acme%20corp", f.admin,
		`{"tier":"paid"}`, false); code != http.StatusOK {
		t.Fatalf("a principal a token may carry = %d %s, want 200", code, body)
	}
	if code, body := f.request(t, http.MethodPut,
		"/api/v1/storage/quotas/"+strings.Repeat("x", store.StoragePrincipalMaxLen+1), f.admin,
		`{"tier":"paid"}`, false); code != http.StatusBadRequest {
		t.Fatalf("an over-long principal = %d %s, want 400", code, body)
	}
	if code, body := f.request(t, http.MethodPut, "/api/v1/storage/quotas/acme", f.admin,
		`{"tier":"platinum"}`, false); code != http.StatusBadRequest {
		t.Fatalf("an unknown tier = %d %s, want 400", code, body)
	}

	for _, refused := range []struct {
		method, path, body string
	}{
		{http.MethodPut, "/api/v1/storage/settings", `{"event_retention_days":1}`},
		{http.MethodPut, "/api/v1/storage/quotas/acme", `{"tier":"free"}`},
	} {
		code, body := f.request(t, refused.method, refused.path, f.team, refused.body, false)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s on a runs.state token = %d %s, want 403 for want of admin",
				refused.method, refused.path, code, body)
		}
	}
}

func TestStorageShowSeparatesTheAdminViewFromTheTeamView(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1024})
	f.server.MaintainStorage(context.Background())

	type state struct {
		Quota    map[string]any   `json:"quota"`
		Usage    map[string]any   `json:"usage"`
		Database map[string]any   `json:"database"`
		Alarm    bool             `json:"alarm"`
		Quotas   []map[string]any `json:"quotas"`
	}
	code, body := f.request(t, http.MethodGet, "/api/v1/storage", f.team, "", false)
	if code != http.StatusOK {
		t.Fatalf("team read = %d %s, want 200", code, body)
	}
	var asTeam state
	if err := json.Unmarshal([]byte(body), &asTeam); err != nil {
		t.Fatalf("decode team view: %v", err)
	}
	if asTeam.Quota["principal"] != "acme" {
		t.Fatalf("team view quota = %+v, want acme's own", asTeam.Quota)
	}
	if asTeam.Database != nil || len(asTeam.Quotas) > 0 {
		t.Fatalf("team view leaks the admin block: database=%v quotas=%v", asTeam.Database, asTeam.Quotas)
	}
	if asTeam.Usage["month"] == "" {
		t.Fatal("team view carries no month")
	}

	code, body = f.request(t, http.MethodGet, "/api/v1/storage", f.admin, "", false)
	if code != http.StatusOK {
		t.Fatalf("admin read = %d %s, want 200", code, body)
	}
	var asAdmin state
	if err := json.Unmarshal([]byte(body), &asAdmin); err != nil {
		t.Fatalf("decode admin view: %v", err)
	}
	if asAdmin.Database == nil || asAdmin.Database["total_bytes"] == nil {
		t.Fatalf("admin view carries no database sample: %s", body)
	}
	if len(asAdmin.Quotas) != 1 || asAdmin.Quotas[0]["principal"] != "acme" {
		t.Fatalf("admin view quota rows = %v, want acme's", asAdmin.Quotas)
	}
}

func TestHealthPublishesTheAlarmAndNoSizes(t *testing.T) {
	f := newStorageFixture(t)
	if err := f.store.SetStorageSettings(context.Background(), store.StorageSettings{
		DatabaseAlarmBytes: 1,
	}); err != nil {
		t.Fatalf("set an alarm that any database passes: %v", err)
	}
	f.server.MaintainStorage(context.Background())

	code, body := f.request(t, http.MethodGet, "/api/v1/health", "", "", false)
	if code != http.StatusOK {
		t.Fatalf("health = %d %s, want 200", code, body)
	}
	var health struct {
		Status   string         `json:"status"`
		Database map[string]any `json:"database"`
	}
	if err := json.Unmarshal([]byte(body), &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health.Database["sampled"] != true {
		t.Fatalf("health = %s, want a sampled database field after one maintenance pass", body)
	}
	if health.Database["alarm"] != true {
		t.Fatalf("health database = %v, want the alarm raised", health.Database)
	}
	for _, leaked := range []string{"total_bytes", "largest_tables", "alarm_bytes", "tables"} {
		if _, ok := health.Database[leaked]; ok {
			t.Fatalf("the unauthenticated health route publishes %q: %v", leaked, health.Database)
		}
	}
	if health.Status != "ok" {
		t.Fatalf("health status = %q, want ok; the alarm must not degrade a working controller", health.Status)
	}
}
