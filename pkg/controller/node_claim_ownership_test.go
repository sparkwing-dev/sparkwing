package controller_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		{"revoke-ready", "/api/v1/runs/run-1/nodes/only/revoke-ready", ""},
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

func TestNodeClaimOwnership_MarkReadyIsAdminOnly(t *testing.T) {
	f := newOwnershipFixture(t)
	for _, token := range []struct{ name, raw string }{
		{"claiming runner", f.owner},
		{"stranger", f.stranger},
	} {
		got := f.post(t, token.raw, "/api/v1/runs/run-1/nodes/only/mark-ready", "")
		if got != http.StatusForbidden {
			t.Errorf("%s POST mark-ready = %d, want 403", token.name, got)
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
