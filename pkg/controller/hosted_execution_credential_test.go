package controller_test

import (
	"bytes"
	"context"
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

func TestFinalizeNodeReadyPolicyIsStrictAndUnwired(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	seedRepoTrigger(t, f.store, "policy-run", "acme/repo")
	c := client.NewWithToken(f.url, nil, raw)
	trigger, err := c.ClaimTrigger(context.Background())
	if err != nil || trigger == nil {
		t.Fatalf("claim trigger = %+v, %v", trigger, err)
	}
	seedRunNode(t, f.store, trigger.ID, "build")
	held := store.WithTriggerClaimFence(context.Background(), store.TriggerClaimFence{ClaimGeneration: trigger.ClaimSeq})
	if _, err := c.FinalizeNodeReadyWithPolicy(held, trigger.ID, "build", store.DispatchHosted); err == nil ||
		!strings.Contains(err.Error(), "503") {
		t.Fatalf("hosted policy without dispatcher = %v, want 503", err)
	}
	n, err := f.store.GetNode(context.Background(), trigger.ID, "build")
	if err != nil || n.Claimed || n.ReadyAt != nil {
		t.Fatalf("unwired hosted policy changed node: %+v, %v", n, err)
	}

	req, err := http.NewRequest(http.MethodPost,
		f.url+"/api/v1/runs/"+trigger.ID+"/nodes/build/finalize-ready",
		bytes.NewBufferString(`{"dispatch_policy":"bogus"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(store.TriggerGenerationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown policy status = %d, want 400", resp.StatusCode)
	}
}

func TestHostedExecutionCredentialDelegatesClaimAndRejectsOtherResources(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "bound-run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "bound-run", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "other-run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	poolRaw, pool, err := st.CreateTokenWith(ctx, "cloud-pool", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, time.Now(), store.TokenOptions{Metered: true})
	if err != nil || poolRaw == "" {
		t.Fatalf("create pool token: %v", err)
	}
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "bound-payment", "admin"); err != nil {
		t.Fatal(err)
	}
	result, err := st.FinalizeExecutorClaimRound(ctx, "bound-run", "build", store.DispatchHosted, &store.HostedClaimSpec{
		Binding: store.ExecutionCredentialBinding{
			RunID: "bound-run", RootNodeID: "build",
			DelegatedPrincipal: pool.Principal, DelegatedTokenPrefix: pool.Prefix,
		},
		Scopes: []string{controller.ScopeNodesClaim, controller.ScopeRunsState},
		TTL:    time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)

	bound := client.NewWithToken(srv.URL, nil, result.Hosted.RawBearer)
	if _, err := bound.GetRunForExecution(ctx, "bound-run"); err != nil {
		t.Fatalf("read bound run through delegated claim: %v", err)
	}
	fenceCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: result.Hosted.Node.ClaimedBy, ClaimGeneration: result.Hosted.Node.ClaimGeneration,
		MembershipID: result.Hosted.Node.ClaimMembershipID, ReservationID: result.Hosted.Node.ReservationID,
	})
	if err := bound.StartNode(fenceCtx, "bound-run", "build"); err != nil {
		t.Fatalf("start bound node through delegated claim: %v", err)
	}

	for _, path := range []string{"/api/v1/runs/other-run", "/api/v1/nodes/claim", "/api/v1/tokens"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if path == "/api/v1/nodes/claim" {
			req.Method = http.MethodPost
		}
		req.Header.Set("Authorization", "Bearer "+result.Hosted.RawBearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", path, resp.StatusCode)
		}
	}

	tok, err := st.LookupToken(result.Hosted.RawBearer, time.Now())
	if err != nil || tok.ExecutionBinding == nil {
		t.Fatalf("bound token lookup = %+v, %v", tok, err)
	}
	if !strings.HasPrefix(tok.Principal, "hosted:swr_") {
		t.Fatalf("technical principal = %q", tok.Principal)
	}
}
