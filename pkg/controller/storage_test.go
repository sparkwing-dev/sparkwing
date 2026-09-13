package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type storageFixture struct {
	url   string
	store *store.Store
	admin string
	team  string
}

func newStorageFixture(t *testing.T, quota store.StorageQuota) storageFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	team, _, err := st.CreateToken("acme", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim, controller.ScopeRunsState, controller.ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("team token: %v", err)
	}
	if quota.Principal != "" {
		if err := st.SetStorageQuota(ctx, quota); err != nil {
			t.Fatalf("set quota: %v", err)
		}
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "p", Status: "running", StartedAt: now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "running"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return storageFixture{url: srv.URL, store: st, admin: admin, team: team}
}

func storageRequest(t *testing.T, method, url, token, contentType string, body io.Reader) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
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

func TestLiveLogAppendRefusedPastTheBytesPerRunQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "root", MaxBytesPerRun: 16})
	url := f.url + "/api/v1/runs/r1/nodes/n1/logs"
	if code, body := storageRequest(t, http.MethodPost, url, f.admin, "text/plain",
		strings.NewReader("0123456789abcdef")); code != http.StatusNoContent {
		t.Fatalf("a write inside the quota = %d %s, want 204", code, body)
	}
	code, body := storageRequest(t, http.MethodPost, url, f.admin, "text/plain", strings.NewReader("more"))
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the write past the quota = %d %s, want 413", code, body)
	}
	if !strings.Contains(body, store.StorageLimitBytesPerRun) || !strings.Contains(body, "root") {
		t.Fatalf("the refusal reads %q and names neither the limit nor the team", body)
	}
}

func TestEventAppendRefusedPastTheBytesPerMonthQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "root", MaxBytesPerMonth: 8})
	url := f.url + "/api/v1/runs/r1/events"
	payload := func(raw []byte) io.Reader {
		buf, err := json.Marshal(map[string]any{"kind": "note", "node_id": "n1", "payload": raw})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return bytes.NewReader(buf)
	}
	code, body := storageRequest(t, http.MethodPost, url, f.admin, "application/json",
		payload([]byte(`"0123456789"`)))
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the event past the month quota = %d %s, want 413", code, body)
	}
	if !strings.Contains(body, store.StorageLimitBytesPerMonth) {
		t.Fatalf("the refusal reads %q and does not name the monthly limit", body)
	}
}

func TestArtifactManifestRefusedPastTheObjectsPerRunQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "root", MaxObjectsPerRun: 1})
	url := f.url + "/api/v1/runs/r1/nodes/n1/artifact-manifest"
	send := func() (int, string) {
		return storageRequest(t, http.MethodPost, url, f.admin, "application/json",
			strings.NewReader(`{"manifest_digest":"sha256:abc"}`))
	}
	if code, body := send(); code != http.StatusNoContent {
		t.Fatalf("the first manifest = %d %s, want 204", code, body)
	}
	code, body := send()
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the second manifest = %d %s, want 413", code, body)
	}
	if !strings.Contains(body, store.StorageLimitObjectsPerRun) {
		t.Fatalf("the refusal reads %q and does not name the object limit", body)
	}
}

func TestStorageWritesUnboundedWithoutAQuota(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{})
	url := f.url + "/api/v1/runs/r1/nodes/n1/logs"
	for range 3 {
		if code, body := storageRequest(t, http.MethodPost, url, f.admin, "text/plain",
			strings.NewReader(strings.Repeat("x", 4096))); code != http.StatusNoContent {
			t.Fatalf("an unquotaed write = %d %s, want 204", code, body)
		}
	}
	usage, err := f.store.StorageUsageFor(context.Background(), "root", "r1", time.Now().UTC())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.RunBytes != 0 {
		t.Fatalf("usage = %+v, want nothing counted while quotas are off", usage)
	}
}

func TestStorageSettingsAndQuotaRoutes(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{})
	code, body := storageRequest(t, http.MethodPut, f.url+"/api/v1/storage/settings", f.admin,
		"application/json", strings.NewReader(
			`{"event_retention_days":30,"node_metric_retention_days":14,"backup_retention_days":30,`+
				`"database_alarm_bytes":1073741824,"default_tier":"free"}`))
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

	code, body = storageRequest(t, http.MethodPut, f.url+"/api/v1/storage/quotas/acme", f.admin,
		"application/json", strings.NewReader(`{"tier":"paid"}`))
	if code != http.StatusOK {
		t.Fatalf("quota write = %d %s, want 200", code, body)
	}
	quota, err := f.store.StorageQuotaFor(context.Background(), "acme")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.MaxBytesPerRun != store.PaidTierQuota.MaxBytesPerRun {
		t.Fatalf("quota = %+v, want the paid tier limits", quota)
	}

	code, body = storageRequest(t, http.MethodGet, f.url+"/api/v1/storage", f.team, "", nil)
	if code != http.StatusOK {
		t.Fatalf("storage read = %d %s, want 200", code, body)
	}
	if !strings.Contains(body, `"max_bytes_per_run"`) || !strings.Contains(body, `"month"`) {
		t.Fatalf("storage read = %s, want the quota and the month's usage", body)
	}

	code, body = storageRequest(t, http.MethodPut, f.url+"/api/v1/storage/settings", f.team,
		"application/json", strings.NewReader(`{"event_retention_days":1}`))
	if code != http.StatusForbidden {
		t.Fatalf("a non-admin settings write = %d %s, want 403", code, body)
	}
}

func TestHealthCarriesTheDatabaseSizeField(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{})
	code, body := storageRequest(t, http.MethodGet, f.url+"/api/v1/health", f.admin, "", nil)
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
	if health.Database == nil {
		t.Fatalf("health = %s, want a database field", body)
	}
	if health.Status != "ok" {
		t.Fatalf("health status = %q, want ok; the size field must not degrade a healthy controller", health.Status)
	}
}
