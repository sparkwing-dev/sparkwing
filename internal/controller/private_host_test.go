package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type localHostRoundTripper struct{ handler http.Handler }

func (t localHostRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func FuzzLocalHostCloseRetiresProcessClients(f *testing.F) {
	for operation := range uint8(8) {
		f.Add(operation, "closed host")
	}
	f.Fuzz(func(t *testing.T, operation uint8, value string) {
		_, host, _, principal := localHostFixture(t)
		process, err := host.ProcessClient(t.Context(), principal, "job")
		if err != nil {
			t.Fatal(err)
		}
		if err := host.Close(); err != nil {
			t.Fatal(err)
		}
		switch operation % 8 {
		case 0:
			err = process.Annotation(t.Context(), nodeproto.AnnotationRequest{Level: "info", Message: value})
		case 1:
			err = process.Summary(t.Context(), nodeproto.SummaryRequest{Markdown: value})
		case 2:
			_, _, err = process.Secret(t.Context(), "secret")
		case 3:
			_, _, err = process.OIDC(t.Context(), value)
		case 4:
			_, err = process.Output(t.Context(), "run", "job")
		case 5:
			_, _, err = process.Latest(t.Context(), "ci", "job", time.Hour)
		case 6:
			_, err = process.StartChild(t.Context(), "ci", map[string]string{"value": value}, "", "")
		case 7:
			_, err = process.Child(t.Context(), "child", 0)
		}
		if err == nil || !strings.Contains(err.Error(), "local host client is closed") {
			t.Fatalf("retained process operation %d reused a closed host: %v", operation%8, err)
		}
	})
}

func localHostFixture(t *testing.T, opts ...store.Option) (*serverFixture, *client.LocalHostClient, *store.Tenant, capability.Principal) {
	t.Helper()
	server, host, tenant, principal, _ := localHostAwardFixture(t, opts...)
	return server, host, tenant, principal
}

func localHostAwardFixture(t *testing.T, opts ...store.Option) (*serverFixture, *client.LocalHostClient, *store.Tenant, capability.Principal, string) {
	t.Helper()
	engine, tenant := engineRuntimeStore(t, opts...)
	engineRuntimeClaim(t, engine, capability.UnitNode)
	engineSQL(t, engine.Store, `DELETE FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`)
	engine.claimProfiles = engineProfileFake{}
	award, err := engine.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: "run"}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := engine.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(award.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	server := New(engine.Store, nil)
	host, err := client.NewLocalHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return string(award.Bearer), nil }}, "run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return &serverFixture{Server: server, store: engine.Store, engine: engine}, host, tenant, principal, string(award.Bearer)
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
	claim, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "build"}, Actor: actor, Policy: store.SourceNone})
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
	debug, err := client.NewDebugReader(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return string(claim.Bearer), nil }})
	if err != nil {
		t.Fatal(err)
	}
	current, err := debug.ReadDebugJob(t.Context(), "1", "build")
	if err != nil || current.Execution == nil || current.Execution.Kind != "runner" || current.Execution.Runner != owner.Capability.Subject.ID {
		t.Fatalf("current persisted executor: %+v %v", current, err)
	}
	var ownerBefore, ownerAfter int64
	if err := teststore.SQL(t, server.store).QueryRowContext(t.Context(), `SELECT lease_expires_at FROM triggers WHERE team='a' AND id='run'`).Scan(&ownerBefore); err != nil {
		t.Fatal(err)
	}
	beat, err := host.RenewNode(t.Context(), claim.Node.Ref, nodeActor, true)
	if err != nil || beat.ExpiresAt.IsZero() {
		t.Fatalf("independent node renewal: %+v %v", beat, err)
	}
	if err := teststore.SQL(t, server.store).QueryRowContext(t.Context(), `SELECT lease_expires_at FROM triggers WHERE team='a' AND id='run'`).Scan(&ownerAfter); err != nil {
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
			if err := teststore.SQL(t, server.store).QueryRowContext(t.Context(), `SELECT status FROM triggers WHERE team='a' AND id='run'`).Scan(&status); err != nil || status != "done" {
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
	if err := teststore.SQL(t, server.store).QueryRowContext(t.Context(), `SELECT status FROM triggers WHERE team='a' AND id='run'`).Scan(&status); err != nil || status != "claimed" {
		t.Fatalf("owner changed: %s %v", status, err)
	}
}

func TestLocalHostPrivateTransportDrainsCancelledOwner(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "build"}
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
	for replay := range 2 {
		applied, err := host.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: engineActor(a), To: store.NodeCancelled, Cause: store.CauseCancel, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: "cancel_requested"}}})
		if err != nil || !applied.CancelRequested || applied.Replayed != (replay == 1) {
			t.Fatalf("owner cancellation %d: %+v %v", replay, applied, err)
		}
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
		node := store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "build"}, OwnerCapabilityPrefix: "owner", CapabilityPrefix: "child", Generation: 1, ClaimedBy: "host"}
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

func TestLocalHostChildIntakeResolvesCapturedDefaults(t *testing.T) {
	server, _, tenant, owner := localHostFixture(t)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','build','pending',1,?,?)`, []byte(`{"id":"build","children":[{"pipeline":"weather-report"}]}`), []byte(`{}`))
	a := capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	claim, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "build"}, Actor: engineActor(a), Policy: store.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	options, err := settings.ResolveRequest(settings.Inputs{Context: settings.Context{Local: true, NumCPU: 2}})
	if err != nil {
		t.Fatal(err)
	}
	body := controllerwire.CreateRunRequest{IdempotencyKey: "child", Request: runspec.Request{Version: 1, Team: "a", Pipeline: "weather-report", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Origin: runspec.OriginChild, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: "0123456789abcdef0123456789abcdef01234567"}, Source: runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567"}, Options: options, Lineage: runspec.Lineage{ParentRunID: "run", ParentJobID: "build"}}}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/runs/run/children", bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+string(claim.Bearer))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("children HTTP %d: %s", response.Code, response.Body)
	}
	var admitted controllerwire.CreateRunResponse
	if err := json.Unmarshal(response.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	child, err := tenant.ReadRun(t.Context(), admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	timeout := child.Request.Request.Options.OwnerExecutionTimeout
	if !timeout.Set || timeout.Value != limits.RunDeadline {
		t.Fatalf("child timeout: %+v", timeout)
	}
}

type cancelSnapshotBinding struct {
	capability.BindingChecker
	cancel context.CancelFunc
}

func (b cancelSnapshotBinding) CheckBinding(ctx context.Context, p capability.Principal, rule capability.BindingRule, target capability.Target) error {
	if err := b.BindingChecker.CheckBinding(ctx, p, rule, target); err != nil {
		return err
	}
	b.cancel()
	return nil
}

func TestLocalHostSnapshotFailuresLogStoreCauseOnce(t *testing.T) {
	for _, stage := range []string{"store", "state", "handler_cancel", "authentication_cancel"} {
		t.Run(stage, func(t *testing.T) {
			engine, _ := engineRuntimeStore(t)
			engineRuntimeClaim(t, engine, capability.UnitNode)
			engine.claimProfiles = engineProfileFake{}
			award, err := engine.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: "run"}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			server := New(engine.Store, slog.New(slog.NewJSONHandler(&logs, nil)))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantStatus, wantCause := 500, "invalid character"
			switch stage {
			case "store":
				engineSQL(t, engine.Store, `UPDATE nodes SET execution_json=?`, []byte(`!`))
			case "state":
				engineSQL(t, engine.Store, `UPDATE nodes SET status='running',attempt=0`)
				wantCause = "running job has no claim or attempt"
			case "handler_cancel":
				server.authorization.Bindings = cancelSnapshotBinding{server.authorization.Bindings, cancel}
				wantCause = "context canceled"
			case "authentication_cancel":
				cancel()
				wantStatus, wantCause = 503, "context canceled"
			}
			r := httptest.NewRequest("GET", "/api/v1/runs/run/local-host/snapshot", nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+string(award.Bearer))
			r.Header.Set(RequestIDHeader, "snapshot-request")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			records := auditLines(t, &logs, "http request failed")
			if w.Code != wantStatus || len(records) != 1 || records[0]["request_id"] != "snapshot-request" || !strings.Contains(records[0]["err"].(string), wantCause) || records[0]["route"] != "/api/v1/runs/{run}/local-host/snapshot" {
				t.Fatalf("snapshot HTTP %d: %s logs=%s", w.Code, w.Body, logs.String())
			}
			if strings.Contains(w.Body.String(), wantCause) {
				t.Fatalf("snapshot exposes private cause: %s", w.Body)
			}
		})
	}
}
