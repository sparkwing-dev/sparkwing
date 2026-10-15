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
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	runnerScopes := []string{
		controller.ScopeNodesClaim, controller.ScopeRunsState,
		controller.ScopeRunsRead,
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
		if err := storageTestTenant(t, st).SetStorageQuota(ctx, q); err != nil {
			t.Fatalf("set quota for %s: %v", q.Team, err)
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

func (f storageFixture) monthUsage(t *testing.T) store.StorageUsage {
	t.Helper()
	usage, err := storageTestTenant(t, f.store).StorageUsageFor(context.Background(), "run-1",
		store.StorageMonth(time.Now().UTC()))
	if err != nil {
		t.Fatalf("usage: %v", err)
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

// An event's bytes are its kind, "note", plus its payload.
func TestEventAppendRefusedPastTheBytesPerRunQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerRun: 12})
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
	if got := f.monthUsage(t).RunBytes; got != 12 {
		t.Fatalf("charged bytes = %d, want 12; the refused event was charged", got)
	}
}

func TestEventAppendRefusedPastTheBytesPerMonthQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerMonth: 4})
	code, body := f.request(t, http.MethodPost, storageEventPath, f.team, eventBody("12345"), true)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the event past the month quota = %d %s, want 413", code, body)
	}
	if !strings.Contains(body, store.StorageLimitBytesPerMonth) {
		t.Fatalf("the refusal reads %q and does not name the monthly limit", body)
	}
}

func TestArtifactManifestRefusedPastTheObjectsPerRunQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Team: store.DefaultTeam, MaxObjectsPerRun: 1})
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

func TestLiveLogAppendIsNotChargedToStorage(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerRun: 8})
	path := "/api/v1/runs/run-1/nodes/only/logs"
	for range 3 {
		if code, body := f.request(t, http.MethodPost, path, f.team,
			strings.Repeat("x", 4096), true); code != http.StatusNoContent {
			t.Fatalf("a live-log append = %d %s, want 204; the in-memory ring is not durable storage", code, body)
		}
	}
	if got := f.monthUsage(t); got.RunBytes != 0 || got.MonthBytes != 0 {
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
	if got := f.monthUsage(t); got.RunBytes != 0 {
		t.Fatalf("usage = %+v, want nothing counted while quotas are off", got)
	}
}

func TestHealthPublishesTheAlarmAndNoSizes(t *testing.T) {
	f := newStorageFixture(t)
	if err := f.store.AsOperator().SetStorageSettings(context.Background(), store.StorageSettings{
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

// A runner's principal name is whatever an editor typed, so two teams can
// both run "agent:eddie". Storage is charged to the writer's team, so one
// team's writes never spend a quota or a month another team is held to.

func TestEventAppendRefusesAnUnboundedKind(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerRun: 1 << 20})
	buf, err := json.Marshal(map[string]any{
		"kind": strings.Repeat("k", store.MaxEventKindBytes+1), "node_id": "only", "payload": []byte("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := f.request(t, http.MethodPost, storageEventPath, f.team, string(buf), true); code != http.StatusBadRequest {
		t.Fatalf("an event with a %d-byte kind = %d %s, want 400", store.MaxEventKindBytes+1, code, body)
	}
	if got := f.monthUsage(t).RunBytes; got != 0 {
		t.Fatalf("a refused kind charged %d bytes", got)
	}
}

func storageTestTenant(t *testing.T, s *store.Store) *store.Tenant {
	t.Helper()
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	return tenant
}
