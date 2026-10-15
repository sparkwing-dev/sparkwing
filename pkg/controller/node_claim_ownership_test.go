package controller_test

import (
	"context"
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

type ownershipFixture struct {
	url      string
	owner    string
	stranger string
	store    *store.Store
	fence    store.NodeClaimFence
}

func newOwnershipFixture(t *testing.T) ownershipFixture {
	return newOwnershipFixtureWithScopes(t, []string{"nodes.claim"})
}

func newOwnershipFixtureWithScopes(t *testing.T, scopes []string) ownershipFixture {
	t.Helper()
	return newOwnershipFixtureWith(t, scopes, nil)
}

func newOwnershipFixtureWith(t *testing.T, scopes []string, configure func(*controller.Server) *controller.Server) ownershipFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	owner, _, err := st.CreateToken("runner-a", store.TokenKindRunner,
		scopes, 0, now)
	if err != nil {
		t.Fatalf("CreateToken owner: %v", err)
	}
	stranger, _, err := st.CreateToken("runner-b", store.TokenKindRunner,
		scopes, 0, now)
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}

	seedRunNode(t, st, "run-1", "only")
	ctx := context.Background()
	if err := st.MarkNodeReady(ctx, "run-1", "only"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	c := controller.New(st, nil)
	if configure != nil {
		c = configure(c)
	}
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)

	n, err := client.NewWithToken(srv.URL, nil, owner).
		ClaimNode(ctx, "runner:box-a:1", nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("owner ClaimNode: %v", err)
	}
	if n == nil || n.NodeID != "only" {
		t.Fatalf("owner claimed %+v, want node only", n)
	}
	return ownershipFixture{
		url: srv.URL, owner: owner, stranger: stranger, store: st,
		fence: store.NodeClaimFence{
			HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
			ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		},
	}
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
	if token == f.owner {
		req.Header.Set(store.ClaimHolderHeader, f.fence.HolderID)
		req.Header.Set(store.ClaimMembershipHeader, f.fence.MembershipID)
		req.Header.Set(store.ClaimReservationHeader, f.fence.ReservationID)
		req.Header.Set(store.ClaimGenerationHeader, strconv.FormatInt(f.fence.ClaimGeneration, 10))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
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

