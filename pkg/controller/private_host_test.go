package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type localHostRoundTripper struct{ handler http.Handler }

func (t localHostRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func localHostFixture(t *testing.T) (*Server, *client.LocalHostClient, *store.Tenant, capability.Principal) {
	t.Helper()
	engine, tenant := engineRuntimeStore(t)
	engineRuntimeClaim(t, engine, capability.UnitNode)
	engineSQL(t, engine.Store, `DELETE FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`)
	engine.Store.SetClaimProfileResolver(engineProfileFake{})
	award, err := engine.Store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: "run"}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := engine.Store.LookupCapability(t.Context(), sha256.Sum256(award.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	server := New(engine.Store, nil)
	host, err := client.NewLocalHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return string(award.Bearer), nil }}, "run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return server, host, tenant, principal
}

func TestLocalHostPrivateTransportUsesPersistedOwnerAndNodeAwards(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	principal, err := host.Principal(t.Context())
	if err != nil || principal.Authority != owner.Authority || principal.Capability.Digest != owner.Capability.Digest {
		t.Fatalf("owner refresh: %+v %v", principal, err)
	}
	snapshot, err := host.ReadSnapshot(t.Context(), "run")
	if err != nil || snapshot.Run.ID != "run" || len(snapshot.Nodes) != 0 {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','build','pending',1,?,?)`, []byte(`{"id":"build"}`), []byte(`{}`))
	authorized := capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	actor := engineActor(authorized)
	claim, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: store.NodeRef{RunID: "run", NodeID: "build"}, Actor: actor, Policy: store.SourceNone})
	if err != nil || claim.Kind != store.Execute {
		t.Fatalf("node award: %+v %v", claim, err)
	}
	if err := host.AdoptClaim(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	nodeActor := store.Actor{Kind: store.ActorHolder, CapabilityPrefix: claim.Node.CapabilityPrefix, Generation: claim.Node.Generation}
	started, err := host.TransitionNode(t.Context(), store.NodeTransition{Ref: claim.Node.Ref, Actor: nodeActor, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: new(int64(0))})
	if err != nil || started.Attempt != 1 {
		t.Fatalf("start: %+v %#v", started, err)
	}
	var ownerBefore, ownerAfter int64
	if err := server.store.DB().QueryRowContext(t.Context(), `SELECT lease_expires_at FROM triggers WHERE team='a' AND id='run'`).Scan(&ownerBefore); err != nil {
		t.Fatal(err)
	}
	beat, err := host.RenewNode(t.Context(), claim.Node.Ref, nodeActor, true)
	if err != nil || beat.ExpiresAt.IsZero() {
		t.Fatalf("independent node renewal: %+v %v", beat, err)
	}
	if err := server.store.DB().QueryRowContext(t.Context(), `SELECT lease_expires_at FROM triggers WHERE team='a' AND id='run'`).Scan(&ownerAfter); err != nil {
		t.Fatal(err)
	}
	renewed, err := tenant.ReadNode(t.Context(), claim.Node.Ref)
	if err != nil || renewed.LeaseExpiresAt == nil || !renewed.LeaseExpiresAt.Equal(beat.ExpiresAt) || ownerAfter != ownerBefore {
		t.Fatalf("node renewal changed owner lease: before=%d after=%d node=%+v error=%v", ownerBefore, ownerAfter, renewed, err)
	}
	node, err := host.NodeController(t.Context(), claim.Node.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.RecordStep(t.Context(), "run", "build", controllerwire.StepReportRequest{StepID: "compile", State: "running", SourceAttempt: started.Attempt}); err != nil {
		t.Fatal(err)
	}
	change := store.NodeTransition{Ref: claim.Node.Ref, Actor: nodeActor, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: &started.Attempt, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeSuccess}}}
	result, err := host.TransitionNode(t.Context(), change)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.StateSeq == 0 {
		t.Fatalf("result was not committed: %+v", result)
	}
	row, err := tenant.ReadNode(t.Context(), claim.Node.Ref)
	if err != nil || row.StateActor.Authority.ID != claim.Node.CapabilityPrefix || row.StateActor.Kind != store.ActorHolder || row.Outcome != store.NodeSuccess {
		t.Fatalf("server actor: %+v %v", row.StateActor, err)
	}
}

func TestLocalHostFinishPreservesPolicyAndCompletesOwner(t *testing.T) {
	for _, cause := range []store.RunCause{store.RunFinish, store.RunPolicyRefused} {
		t.Run(string(cause), func(t *testing.T) {
			server, host, tenant, owner := localHostFixture(t)
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			spec, err := json.Marshal(runspec.Spec{Version: 1, Request: run.Request, Resolved: runspec.Resolved{Guards: runspec.Guards{Passed: true}}})
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, server.store, `UPDATE runs SET spec_json=?,spec_hash='spec',sealed_at=1,plan_json=?,plan_hash='plan' WHERE team='a' AND id='run'`, spec, []byte(`{"jobs":[]}`))
			a := capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
			actor := engineActor(a)
			change := store.RunTransition{RunID: "run", Actor: actor, To: store.RunSuccess, Cause: cause}
			if cause == store.RunPolicyRefused {
				change.OwnerPolicy = &store.OwnerPolicyResult{Kind: store.PolicyAdmissionSkip, Generation: actor.Owner.Generation}
			}
			if _, err := host.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Transition: change, Unit: actor.Owner}); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := server.store.DB().QueryRowContext(t.Context(), `SELECT status FROM triggers WHERE team='a' AND id='run'`).Scan(&status); err != nil || status != "done" {
				t.Fatalf("owner completion: %s %v", status, err)
			}
			run, err = tenant.ReadRun(t.Context(), "run")
			if err != nil || run.State != store.RunSuccess || run.FinishedAt == nil {
				t.Fatalf("run completion: %+v %v", run, err)
			}
		})
	}
}

func TestLocalHostRunTransitionCannotLeaveTerminalOwnerClaimed(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	for _, state := range []store.RunState{store.RunSuccess, store.RunFailed, store.RunCancelled} {
		for _, cause := range []store.RunCause{store.RunFinish, store.RunPublish, store.RunPolicyRefused, store.RunCancel} {
			if _, err := host.TransitionRun(t.Context(), store.RunTransition{RunID: "run", To: state, Cause: cause}); err == nil {
				t.Fatalf("terminal edge bypassed owner completion: %s %s", state, cause)
			}
		}
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunRunning || run.FinishedAt != nil {
		t.Fatalf("refusal mutated run: %+v %v", run, err)
	}
	if _, err := tenant.ReadOwnerSnapshot(t.Context(), owner.Authority, "run"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := server.store.DB().QueryRowContext(t.Context(), `SELECT status FROM triggers WHERE team='a' AND id='run'`).Scan(&status); err != nil || status != "claimed" {
		t.Fatalf("owner changed: %s %v", status, err)
	}
}

func TestLocalHostPrivateTransportDrainsCancelledOwner(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	ref := store.NodeRef{RunID: "run", NodeID: "build"}
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','build','pending',1,?,?)`, []byte(`{"id":"build"}`), []byte(`{}`))
	a := capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	claim, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: engineActor(a), Policy: store.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.AdoptClaim(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{Kind: store.ActorHolder, CapabilityPrefix: claim.Node.CapabilityPrefix, Generation: claim.Node.Generation}
	started, err := host.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: actor, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: new(int64(0))})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `UPDATE runs SET cancel_requested_at=1,cancel_reason='user_cancelled' WHERE team='a' AND id='run'`)
	snapshot, err := host.ReadSnapshot(t.Context(), "run")
	if err != nil || snapshot.Run.CancelRequestedAt == nil {
		t.Fatalf("cancel snapshot: %v", err)
	}
	node, err := host.NodeController(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.RecordStep(t.Context(), "run", "build", controllerwire.StepReportRequest{StepID: "compile", State: "cancelled", SourceAttempt: started.Attempt}); err != nil {
		t.Fatal(err)
	}
	_, err = host.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: actor, To: store.NodeCancelled, Cause: store.CauseReport, SourceAttempt: &started.Attempt, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLocalHostPrivateTransportRejectsForeignCredentialProbesAndUnauthenticatedAccess(t *testing.T) {
	server, host, _, owner := localHostFixture(t)
	for _, lookup := range []capability.CredentialLookup{
		{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte("foreign"))},
		{Class: capability.CredentialSession, Audience: capability.Controller, Digest: owner.Capability.Digest},
		{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: owner.Capability.Digest},
	} {
		_, err := host.ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.Forbidden {
			t.Fatalf("foreign probe: %v", err)
		}
	}
	if err := host.CheckBinding(t.Context(), capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "runner"}}, capability.BindNone, capability.Target{Team: "a", RunID: "run"}); err == nil {
		t.Fatal("owner queried a foreign root binding")
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/runs/run/local-host/snapshot", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("hosted private route status=%d body=%s", w.Code, w.Body.String())
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := host.ReadSnapshot(t.Context(), "run"); err == nil {
		t.Fatal("closed host remained usable")
	}
}

func FuzzLocalHostRejectsForeignAuthority(f *testing.F) {
	f.Add("foreign", uint8(0))
	f.Add("run", uint8(5))
	f.Fuzz(func(t *testing.T, value string, field uint8) {
		owner := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, Capability: capability.Capability{Prefix: "owner", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "host"}, Unit: capability.UnitTrigger, Binding: capability.Binding{RunID: "run", Holder: "host"}}}
		node := store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "build"}, OwnerCapabilityPrefix: "owner", CapabilityPrefix: "child", Generation: 1, ClaimedBy: "host"}
		child := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "child"}, Capability: capability.Capability{Prefix: "child", Team: "a", Subject: owner.Capability.Subject, Unit: capability.UnitNode, Purpose: capability.PurposeWork, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "build", Generation: 1, Holder: "host"}}}
		if !localHostOwnsNode(owner, node, child) {
			t.Fatal("valid independent claim rejected")
		}
		value = "foreign:" + value
		switch field % 10 {
		case 0:
			node.Ref.RunID = value
		case 1:
			node.OwnerCapabilityPrefix = value
		case 2:
			node.ClaimedBy = value
		case 3:
			child.Authority.ID = value
		case 4:
			child.Capability.Team = value
		case 5:
			child.Capability.Subject.ID = value
		case 6:
			child.Capability.Binding.RunID = value
		case 7:
			child.Capability.Binding.NodeID = value
		case 8:
			child.Capability.Binding.Holder = value
		case 9:
			child.Capability.Prefix = value
		}
		raw, err := json.Marshal(child)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &child); err != nil {
			t.Fatal(err)
		}
		if localHostOwnsNode(owner, node, child) {
			t.Fatal("foreign authority crossed the host award boundary")
		}
	})
}
