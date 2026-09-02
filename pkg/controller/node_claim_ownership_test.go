package controller_test

import (
	"context"
	"errors"
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

type ownershipFixture struct {
	url      string
	owner    string
	stranger string
	store    *store.Store
}

func newOwnershipFixture(t *testing.T) ownershipFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	owner, _, err := st.CreateToken("runner-a", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken owner: %v", err)
	}
	stranger, _, err := st.CreateToken("runner-b", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}

	seedRunNode(t, st, "run-1", "only")
	ctx := context.Background()
	if err := st.MarkNodeReady(ctx, "run-1", "only"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)

	n, err := client.NewWithToken(srv.URL, nil, owner).
		ClaimNode(ctx, "runner:box-a:1", nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("owner ClaimNode: %v", err)
	}
	if n == nil || n.NodeID != "only" {
		t.Fatalf("owner claimed %+v, want node only", n)
	}
	return ownershipFixture{url: srv.URL, owner: owner, stranger: stranger, store: st}
}

func (f ownershipFixture) post(t *testing.T, token, path, body string) int {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, f.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestNodeClaimOwnership_StrangerTokenCannotWriteAnotherRunnersNode(t *testing.T) {
	f := newOwnershipFixture(t)

	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{"annotations", "/api/v1/runs/run-1/nodes/only/annotations", `{"message":"hi"}`},
		{"summary", "/api/v1/runs/run-1/nodes/only/summary", `{"markdown":"# hi"}`},
		{"activity", "/api/v1/runs/run-1/nodes/only/activity", `{"detail":"working"}`},
		{"touch", "/api/v1/runs/run-1/nodes/only/touch", ""},
		{"artifact-manifest", "/api/v1/runs/run-1/nodes/only/artifact-manifest", `{"digest":"sha256:abc"}`},
		{"metrics", "/api/v1/runs/run-1/nodes/only/metrics", `{"cpu_nanos":1}`},
		{"dispatch", "/api/v1/runs/run-1/nodes/only/dispatch", `{"seq":0}`},
		{"steps/start", "/api/v1/runs/run-1/nodes/only/steps/start", `{"step_id":"s1","name":"s1"}`},
		{"steps/finish", "/api/v1/runs/run-1/nodes/only/steps/finish", `{"step_id":"s1","outcome":"success"}`},
		{"steps/skip", "/api/v1/runs/run-1/nodes/only/steps/skip", `{"step_id":"s1"}`},
		{"steps/annotations", "/api/v1/runs/run-1/nodes/only/steps/annotations", `{"step_id":"s1","message":"hi"}`},
		{"steps/summary", "/api/v1/runs/run-1/nodes/only/steps/summary", `{"step_id":"s1","markdown":"hi"}`},
		{"bounce/consume", "/api/v1/runs/run-1/nodes/only/bounce/consume", `{"seq":1,"outcome":"bounced"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.post(t, f.stranger, tc.path, tc.body); got != http.StatusForbidden {
				t.Errorf("stranger POST %s = %d, want 403", tc.path, got)
			}
			if got := f.post(t, f.owner, tc.path, tc.body); got == http.StatusForbidden {
				t.Errorf("claiming runner POST %s = 403, want the route to admit it", tc.path)
			}
		})
	}
}

func TestNodeClaimOwnership_ReadinessRoutesAreAdminOnly(t *testing.T) {
	f := newOwnershipFixture(t)
	for _, route := range []string{"mark-ready", "revoke-ready"} {
		for _, token := range []struct{ name, raw string }{
			{"claiming runner", f.owner},
			{"stranger", f.stranger},
		} {
			got := f.post(t, token.raw, "/api/v1/runs/run-1/nodes/only/"+route, "")
			if got != http.StatusForbidden {
				t.Errorf("%s POST %s = %d, want 403", token.name, route, got)
			}
		}
	}
}

func TestNodeClaimOwnership_HeartbeatRefusesAnotherPrincipalsHolderID(t *testing.T) {
	f := newOwnershipFixture(t)
	ctx := context.Background()

	if err := client.NewWithToken(f.url, nil, f.owner).
		HeartbeatNodeClaim(ctx, "run-1", "only", "runner:box-a:1", time.Minute, nil); err != nil {
		t.Fatalf("owner heartbeat: %v", err)
	}
	err := client.NewWithToken(f.url, nil, f.stranger).
		HeartbeatNodeClaim(ctx, "run-1", "only", "runner:box-a:1", time.Minute, nil)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("stranger heartbeat with the owner's holder id = %v, want ErrLockHeld", err)
	}
}

func TestNodeClaimOwnership_ExpiredClaimStopsAdmittingWrites(t *testing.T) {
	f := newOwnershipFixture(t)
	if _, err := f.store.DB().Exec(
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-time.Minute).UnixNano(), "run-1", "only",
	); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	got := f.post(t, f.owner, "/api/v1/runs/run-1/nodes/only/annotations", `{"message":"hi"}`)
	if got != http.StatusForbidden {
		t.Errorf("POST annotations under an expired claim = %d, want 403", got)
	}
}

func (f ownershipFixture) get(t *testing.T, token, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func (f ownershipFixture) leaseExpiry(t *testing.T, runID, nodeID string) time.Time {
	t.Helper()
	var ns int64
	if err := f.store.DB().QueryRow(
		`SELECT COALESCE(lease_expires_at, 0) FROM nodes WHERE run_id = ? AND node_id = ?`,
		runID, nodeID,
	).Scan(&ns); err != nil {
		t.Fatalf("read lease: %v", err)
	}
	return time.Unix(0, ns)
}

func TestNodeClaimOwnership_ServerCapsTheRequestedLease(t *testing.T) {
	f := newOwnershipFixture(t)
	seedRunNode(t, f.store, "run-2", "only")
	if err := f.store.MarkNodeReady(context.Background(), "run-2", "only"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	const dayInSeconds = 86400
	if got := f.post(t, f.stranger, "/api/v1/nodes/claim",
		`{"holder_id":"greedy-1","lease_secs":`+strconv.Itoa(dayInSeconds)+`}`); got != http.StatusOK {
		t.Fatalf("claim with a day-long lease = %d, want 200", got)
	}
	limit := time.Now().Add(store.MaxLeaseDuration).Add(time.Minute)
	if exp := f.leaseExpiry(t, "run-2", "only"); exp.After(limit) {
		t.Errorf("claim lease expires at %s, want no later than %s", exp, limit)
	}

	if got := f.post(t, f.stranger, "/api/v1/runs/run-2/nodes/only/heartbeat",
		`{"holder_id":"greedy-1","lease_secs":`+strconv.Itoa(dayInSeconds)+`}`); got != http.StatusNoContent {
		t.Fatalf("heartbeat with a day-long lease = %d, want 204", got)
	}
	limit = time.Now().Add(store.MaxLeaseDuration).Add(time.Minute)
	if exp := f.leaseExpiry(t, "run-2", "only"); exp.After(limit) {
		t.Errorf("heartbeat lease expires at %s, want no later than %s", exp, limit)
	}
}

func TestNodeClaimOwnership_TwoTokensSharingAPrincipalNameDoNotCrossAuthorize(t *testing.T) {
	f := newOwnershipFixture(t)
	now := time.Now().UTC()
	twin, _, err := f.store.CreateToken("runner-a", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken twin: %v", err)
	}

	if got := f.post(t, twin, "/api/v1/runs/run-1/nodes/only/summary", `{"markdown":"# hi"}`); got != http.StatusForbidden {
		t.Errorf("a second token named runner-a POST summary = %d, want 403", got)
	}
	code, body := f.get(t, twin, "/api/v1/runs/run-1/nodes/only")
	if code != http.StatusForbidden {
		t.Errorf("a second token named runner-a GET node = %d (%s), want 403", code, body)
	}
}

func TestNodeClaimOwnership_CrossRunReadsAndHeartbeatNeedAClaim(t *testing.T) {
	f := newOwnershipFixture(t)
	ctx := context.Background()
	seedRunNode(t, f.store, "run-2", "victim")
	if err := f.store.FinishNode(ctx, "run-2", "victim", "success", "",
		[]byte(`{"private":"other-tenant-output"}`)); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}

	for _, path := range []string{
		"/api/v1/runs/run-2/nodes/victim",
		"/api/v1/runs/run-2/nodes/victim/output",
		"/api/v1/runs/run-2/nodes/victim/bounce",
	} {
		code, body := f.get(t, f.owner, path)
		if code != http.StatusForbidden {
			t.Errorf("GET %s with no claim on run-2 = %d (%s), want 403", path, code, body)
		}
		if strings.Contains(body, "other-tenant-output") {
			t.Errorf("GET %s leaked another run's node output", path)
		}
	}
	if got := f.post(t, f.owner, "/api/v1/runs/run-2/heartbeat", ""); got != http.StatusForbidden {
		t.Errorf("POST run-2 heartbeat with no claim on run-2 = %d, want 403", got)
	}

	if code, body := f.get(t, f.owner, "/api/v1/runs/run-1/nodes/only"); code == http.StatusForbidden {
		t.Errorf("GET the claimed run's own node = 403 (%s), want the route to admit it", body)
	}
	if got := f.post(t, f.owner, "/api/v1/runs/run-1/heartbeat", ""); got == http.StatusForbidden {
		t.Errorf("POST the claimed run's own heartbeat = 403, want the route to admit it")
	}
}

func TestNodeClaimOwnership_ProfilePinIsADispatcherWrite(t *testing.T) {
	f := newOwnershipFixture(t)
	req, err := http.NewRequest(http.MethodPut, f.url+"/api/v1/pipelines/demo/profile/pin",
		strings.NewReader(`{"node_id":"only","cores":8,"memory_bytes":1}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.owner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("claiming runner PUT profile/pin = %d, want 403", resp.StatusCode)
	}
}
