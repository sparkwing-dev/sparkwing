package controller

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestLocalHostChildAuthorityDoesNotReadUnrelatedJobs(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	acceptCapacityPlan(t, server, tenant, owner, "global", "queue", 1, 1, "build", "unrelated")
	actor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
	ref := runspec.NodeRef{RunID: "run", NodeID: "build"}
	if _, err := tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	claim, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: actor, Policy: runstate.SourceNone})
	if err != nil || claim.Kind != runstate.Execute {
		t.Fatalf("node award: %+v %v", claim, err)
	}
	principal, err := server.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, server.store).ExecContext(t.Context(), `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id='unrelated'`, []byte(`not-json`)); err != nil {
		t.Fatal(err)
	}
	target := capability.Target{Team: "a", RunID: "run", NodeID: "build"}
	placementReads := 0
	server.engine.claimProfiles = engineEnrollmentFunc(func(context.Context, capability.Principal) (match.Profile, error) {
		placementReads++
		return match.Profile{}, nil
	})
	t.Run("digest", func(t *testing.T) {
		snapshot, err := host.ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256(claim.Bearer)}, capability.RequestEvidence{Target: target})
		if err != nil {
			t.Fatalf("unrelated job blocked named child authentication: %v", err)
		}
		if len(snapshot.Authorities) == 0 || snapshot.Authorities[0].Principal.Authority != principal.Authority {
			t.Fatalf("wrong child authority: %+v", snapshot.Authorities)
		}
	})
	t.Run("reference", func(t *testing.T) {
		if err := host.CheckBinding(t.Context(), principal, capability.BindOwnNode, target); err != nil {
			t.Fatalf("unrelated job blocked named child binding: %v", err)
		}
	})
	if placementReads != 0 {
		t.Errorf("child authority lookup resolved placement %d times", placementReads)
	}
}
