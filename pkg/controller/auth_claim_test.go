package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestNodeClaim_AuthBlocksUnauthedCaller(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	raw, _, err := st.CreateToken("test-runner", store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).
		EnableAuthFromStore().
		Handler())
	defer srv.Close()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "only", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}

	bare := client.New(srv.URL, nil)
	if err := bare.MarkNodeReady(ctx, "run-1", "only"); err == nil {
		t.Fatal("expected MarkNodeReady without token to fail")
	}

	authed := client.NewWithToken(srv.URL, nil, raw)
	if err := authed.MarkNodeReady(ctx, "run-1", "only"); err == nil {
		t.Fatal("expected MarkNodeReady with a nodes.claim token to fail; readiness is admin-only")
	}
	if err := st.MarkNodeReady(ctx, "run-1", "only"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	n, err := authed.ClaimNode(ctx, "agent-1", nil, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("authed ClaimNode: %v", err)
	}
	if n == nil || n.NodeID != "only" {
		t.Fatalf("wrong claim: %+v", n)
	}

	wrong := client.NewWithToken(srv.URL, nil, "swu_bogusvaluetrailing00000000000000000000000")
	if _, err := wrong.ClaimNode(ctx, "agent-bad", nil, 30*time.Second, nil); err == nil {
		t.Fatal("expected wrong-token claim to fail")
	}
}
