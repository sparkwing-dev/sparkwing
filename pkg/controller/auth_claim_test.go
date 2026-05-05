package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestNodeClaim_AuthBlocksUnauthedCaller stands up a controller with
// a real sw*_ runner token in the tokens table and proves an unauthed
// ClaimNode returns an error carrying 401, while a NewWithToken
// client carrying the real token succeeds.
func TestNodeClaim_AuthBlocksUnauthedCaller(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Seed a runner token BEFORE the server starts so
	// EnableAuthFromStore picks it up.
	raw, _, err := st.CreateToken("test-runner", store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).
		EnableAuthFromStore().
		Handler())
	defer srv.Close()

	// Seed a ready node so claim would succeed if auth allowed it.
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

	// Mark-ready without auth should fail.
	bare := client.New(srv.URL, nil)
	if err := bare.MarkNodeReady(ctx, "run-1", "only"); err == nil {
		t.Fatal("expected MarkNodeReady without token to fail")
	}

	// With the real sw*_ token, mark-ready and claim both succeed.
	authed := client.NewWithToken(srv.URL, nil, raw)
	if err := authed.MarkNodeReady(ctx, "run-1", "only"); err != nil {
		t.Fatalf("authed MarkNodeReady: %v", err)
	}
	n, err := authed.ClaimNode(ctx, "agent-1", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("authed ClaimNode: %v", err)
	}
	if n == nil || n.NodeID != "only" {
		t.Fatalf("wrong claim: %+v", n)
	}

	// A well-formed but unknown sw*_ token fails.
	wrong := client.NewWithToken(srv.URL, nil, "swu_bogusvaluetrailing00000000000000000000000")
	if _, err := wrong.ClaimNode(ctx, "agent-bad", nil, 30*time.Second); err == nil {
		t.Fatal("expected wrong-token claim to fail")
	}
}
