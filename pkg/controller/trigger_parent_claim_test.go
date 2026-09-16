package controller_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type parentClaimFixture struct {
	store                     *store.Store
	owner, twin, other, admin *client.Client
	ownerIdentity             store.ClaimIdentity
	ownerFence                store.NodeClaimFence
}

func newParentClaimFixture(t *testing.T) parentClaimFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	scopes := []string{
		controller.ScopeRunsWrite, controller.ScopeRunsState,
		controller.ScopeNodesClaim, controller.ScopeTriggersClaim,
	}
	now := time.Now().UTC()
	ownerRaw, ownerToken, err := st.CreateToken("tenant-a", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	twinRaw, _, err := st.CreateToken("tenant-a", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	otherRaw, _, err := st.CreateToken("tenant-b", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	adminRaw, _, err := st.CreateToken("ops", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(t.Context(), store.Run{
		ID: "parent", Pipeline: "parent-pipeline", Status: "running", StartedAt: now,
		Repo: "acme/repo", RepoURL: "https://example.invalid/acme/repo.git",
		GitBranch: "main", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(t.Context(), store.Node{RunID: "parent", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: "tenant-a", TokenPrefix: ownerToken.Prefix}
	node, err := st.ClaimNamedNode(t.Context(), identity, "parent", "build", "runner-a", time.Minute,
		store.NamedClaimOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fence := store.NodeClaimFence{
		Claimant: identity, HolderID: node.ClaimedBy, MembershipID: node.ClaimMembershipID,
		ReservationID: node.ClaimReservationID, ClaimGeneration: node.ClaimGeneration,
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return parentClaimFixture{
		store:         st,
		owner:         client.NewWithToken(srv.URL, srv.Client(), ownerRaw),
		twin:          client.NewWithToken(srv.URL, srv.Client(), twinRaw),
		other:         client.NewWithToken(srv.URL, srv.Client(), otherRaw),
		admin:         client.NewWithToken(srv.URL, srv.Client(), adminRaw),
		ownerIdentity: identity, ownerFence: fence,
	}
}

func parentTriggerRequest(runID, nodeID string) client.TriggerRequest {
	return client.TriggerRequest{
		Pipeline: "child-pipeline", ParentRunID: runID, ParentNodeID: nodeID,
		Trigger: client.TriggerMeta{Source: "await-pipeline"},
	}
}

func TestTriggerParentClaim_NodeClaimCreatesRunAndAwaitChild(t *testing.T) {
	f := newParentClaimFixture(t)
	ctx := store.WithNodeClaimFence(t.Context(), f.ownerFence)
	created, err := f.owner.CreateTrigger(ctx, parentTriggerRequest("parent", "build"))
	if err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	child, err := f.store.GetTrigger(t.Context(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentRunID != "parent" || child.ParentNodeID != "build" ||
		child.Repo != "acme/repo" || child.RepoURL != "https://example.invalid/acme/repo.git" ||
		child.GitBranch != "main" || child.GitSHA != strings.Repeat("a", 40) || !child.RepoInherited {
		t.Fatalf("child lineage/provenance = %+v", child)
	}
}

func TestTriggerParentClaim_TriggerClaimCreatesChild(t *testing.T) {
	f := newParentClaimFixture(t)
	now := time.Now().UTC()
	if err := f.store.CreateTrigger(t.Context(), store.Trigger{
		ID: "trigger-parent", Pipeline: "parent-pipeline", Status: "pending", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateRun(t.Context(), store.Run{
		ID: "trigger-parent", Pipeline: "parent-pipeline", Status: "running", StartedAt: now,
		Repo: "acme/repo",
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.ClaimSpecificTriggerFor(
		t.Context(), "trigger-parent", f.ownerIdentity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := store.WithTriggerClaimFence(t.Context(), store.TriggerClaimFence{
		Claimant: f.ownerIdentity, ClaimGeneration: claimed.ClaimSeq,
	})
	if _, err := f.owner.CreateTrigger(ctx, parentTriggerRequest("trigger-parent", "")); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
}

func TestTriggerParentClaim_RefusesAnythingButTheExactLiveParent(t *testing.T) {
	tests := []struct {
		name   string
		client func(parentClaimFixture) *client.Client
		ctx    func(parentClaimFixture) context.Context
		nodeID string
	}{
		{
			"wrong principal", func(f parentClaimFixture) *client.Client { return f.other },
			func(f parentClaimFixture) context.Context { return store.WithNodeClaimFence(t.Context(), f.ownerFence) }, "build",
		},
		{
			"same principal wrong token", func(f parentClaimFixture) *client.Client { return f.twin },
			func(f parentClaimFixture) context.Context { return store.WithNodeClaimFence(t.Context(), f.ownerFence) }, "build",
		},
		{
			"wrong generation", func(f parentClaimFixture) *client.Client { return f.owner },
			func(f parentClaimFixture) context.Context {
				f.ownerFence.ClaimGeneration++
				return store.WithNodeClaimFence(t.Context(), f.ownerFence)
			}, "build",
		},
		{
			"wrong parent node", func(f parentClaimFixture) *client.Client { return f.owner },
			func(f parentClaimFixture) context.Context { return store.WithNodeClaimFence(t.Context(), f.ownerFence) }, "deploy",
		},
		{
			"unclaimed runs.write", func(f parentClaimFixture) *client.Client { return f.owner },
			func(parentClaimFixture) context.Context { return t.Context() }, "build",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newParentClaimFixture(t)
			before, err := f.store.ListTriggers(t.Context(), store.TriggerFilter{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tc.client(f).CreateTrigger(tc.ctx(f), parentTriggerRequest("parent", tc.nodeID))
			if err == nil || (!strings.Contains(err.Error(), "claim_required") && !errors.Is(err, store.ErrLockHeld)) {
				t.Fatalf("CreateTrigger = %v, want a parent claim refusal", err)
			}
			after, listErr := f.store.ListTriggers(t.Context(), store.TriggerFilter{})
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(after) != len(before) {
				t.Fatalf("refused request created a trigger: before=%d after=%d", len(before), len(after))
			}
		})
	}
}

func TestTriggerParentClaim_AdminPreservesOperatorAuthorization(t *testing.T) {
	f := newParentClaimFixture(t)
	if _, err := f.admin.CreateTrigger(t.Context(), parentTriggerRequest("parent", "build")); err != nil {
		t.Fatalf("admin CreateTrigger: %v", err)
	}
}
