package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type runnerAdmissionFunc func(context.Context, string) error

func (f runnerAdmissionFunc) CheckRunner(ctx context.Context, version string) error {
	return f(ctx, version)
}

type engineEnrollmentFunc func(context.Context, capability.Principal) (match.Profile, error)

func (f engineEnrollmentFunc) ResolveClaimProfile(ctx context.Context, principal capability.Principal) (match.Profile, error) {
	return f(ctx, principal)
}

func TestEngineClaimUsesEnrollmentAndChecksRunnerPolicy(t *testing.T) {
	server, tenant := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	root, err := server.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "runner"})
	if err != nil {
		t.Fatal(err)
	}
	authorized := capability.Authorized{Principal: root, Binding: root.Capability.Binding, Target: capability.Target{Team: "a"}}
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{"requires":{"set":true,"value":["team-4cpu"]}}`))
	engineAcceptedFixturePlan(t, server, "a", "run")
	server.claimProfiles = engineEnrollmentFunc(func(_ context.Context, principal capability.Principal) (match.Profile, error) {
		if principal.Authority.ID != "runner" {
			t.Fatal("enrollment received a caller-selected identity")
		}
		return match.Profile{Name: "forged"}, nil
	})
	allowed := false
	server.RunnerPolicy = runnerAdmissionFunc(func(_ context.Context, version string) error {
		if version != "v1.2.3" {
			t.Fatal("runner version was replaced")
		}
		if !allowed {
			return &apierr.Error{Code: apierr.VersionRevoked, Message: "revoked"}
		}
		return nil
	})
	body := controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}, Labels: []string{"name=forged", "class=cloud", "team-4cpu"}}
	if _, err := server.Claim(t.Context(), authorized, body); err == nil {
		t.Fatal("revoked runner received an award")
	}
	if _, err := server.Renew(t.Context(), owner, controllerwire.RenewalRequest{}); err != nil {
		t.Fatalf("runner policy blocked draining: %v", err)
	}
	allowed = true
	got, err := server.Claim(t.Context(), authorized, body)
	if err != nil || got.Claim == nil || got.Claim.Capability == "" || got.Claim.Runner.Name != "runner" || got.Claim.Runner.Type != "runner" || got.Claim.RunID != "run" || got.Claim.JobID != "job" || got.Claim.Purpose != capability.PurposeWork || got.Claim.Delivery != capability.DeliveryDirect || got.Claim.Execution == nil {
		t.Fatalf("registered claim: %+v, %v", got.Claim, err)
	}
	row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.ClaimedBy != "runner" || row.Attempt != 0 || row.Generation != 1 {
		t.Fatalf("persisted award: %+v, %v", row, err)
	}
	if again, err := server.Claim(t.Context(), authorized, body); err != nil || again.Claim != nil {
		t.Fatalf("held award was replayed: %+v, %v", again, err)
	}
}

func TestEngineClaimRequiresTrustedServicesBeforeAward(t *testing.T) {
	fixture, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, fixture, capability.UnitTrigger)
	connection, err := fixture.Store.Connection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := teststore.OpenExisting(strings.TrimPrefix(connection.Target, "sqlite:"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	server := NewEngineController(reopened, EngineController{})
	root, err := reopened.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "runner"})
	if err != nil {
		t.Fatal(err)
	}
	body := controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitNode}}
	for _, configured := range []bool{false, true} {
		if configured {
			server.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
		}
		_, err := server.Claim(t.Context(), capability.Authorized{Principal: root}, body)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.Unavailable {
			t.Fatalf("missing trusted service: %v", err)
		}
	}
	if _, err := server.Renew(t.Context(), owner, controllerwire.RenewalRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineMaterializeOnlyReturnsPrivateExecutionStateWithoutAward(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,spec_hash,execution_json) VALUES ('a','run','job','pending',?,'declaration',?)`, []byte(`{"id":"job"}`), []byte(`{"retry":{"set":true,"value":2}}`))
	got, err := server.ReconcileJob(t.Context(), owner, controllerwire.ReconcileRequest{Policy: "none", MaterializeOnly: true})
	if err != nil || got.Kind != "dispatch" || got.Claim != nil || got.Job.SpecHash != "declaration" || got.Job.Execution.Retry.Value != 2 || got.Job.Attempt != 0 || string(got.Job.Spec) != `{"id":"job"}` {
		t.Fatalf("materialization probe: %+v, %v", got, err)
	}
}

func TestEngineCancelJobKeepsLiveHolderAndRechecksOwner(t *testing.T) {
	server, tenant := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	holder, err := server.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.cancelJob(t.Context(), owner, controllerwire.CancelRequest{Reason: "user_cancelled"})
	if err != nil || !result.CancelRequested {
		t.Fatalf("cancel job: %+v, %v", result, err)
	}
	row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.CapabilityPrefix != holder.Award.CapabilityPrefix || row.CancelRequestedAt == nil || row.CancelReason != "user_cancelled" || row.Outcome != "" {
		t.Fatalf("held result rights: %+v, %v", row, err)
	}
	engineSQL(t, server.Store, `UPDATE triggers SET generation=generation+1 WHERE id='run'`)
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("cancel events: %+v, %v", events, err)
	}
	var cancellation runstate.NodeResult
	if err := json.Unmarshal(events[1].Payload, &cancellation); err != nil || events[1].Kind != "job_cancel_requested" || cancellation.Reason != "user_cancelled" {
		t.Fatalf("cancel reason: %+v, %v", cancellation, err)
	}
	if _, err := server.cancelJob(t.Context(), owner, controllerwire.CancelRequest{Reason: "user_cancelled"}); err == nil {
		t.Fatal("stale owner cancelled a job")
	}
}

func TestEngineReadFailureUsesAuthorizedSourceGeneration(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	failure := nodeproto.JobFailure{Job: "source", Stage: "verify", Error: &apierr.Error{Code: apierr.SDKError, Message: "masked error"}}
	raw, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,generation,finished_at,spec_json,execution_json,failure_json) VALUES ('a','run','source','done','failed',4,1,?,?,?)`, []byte(`{"id":"source"}`), []byte(`{}`), raw)
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','recovery','pending',?,?)`, []byte(`{"id":"recovery","on_failure_of":"source"}`), []byte(`{}`))
	owner.Target.NodeID, owner.Target.SourceGeneration = "source", 4
	got, err := server.readFailure(t.Context(), owner)
	if err != nil || got == nil || got.Job != failure.Job || got.Stage != failure.Stage || got.Error.Code != failure.Error.Code || got.Error.Message != failure.Error.Message {
		t.Fatalf("failure: %+v, %v", got, err)
	}
	owner.Target.SourceGeneration++
	if _, err := server.readFailure(t.Context(), owner); err == nil {
		t.Fatal("requested source generation replaced the stored fence")
	}
}

func TestEngineExpansionReplayReturnsPersistedState(t *testing.T) {
	result := store.AcceptedExpansion{Replayed: true, Nodes: []runstate.NodeRow{{Ref: runspec.NodeRef{RunID: "run", NodeID: "generated"}, Status: runstate.StatusDone, Outcome: runstate.NodeSuccess, Generation: 3, Attempt: 2, StateSeq: 7, Spec: []byte(`{"id":"generated"}`), SpecHash: "hash"}}}
	got, err := expansionResponse(result)
	if err != nil || !got.Replayed || len(got.Jobs) != 1 || got.Jobs[0].Current.Status != "done" || got.Jobs[0].Current.Generation != 3 || got.Jobs[0].Current.Attempt != 2 || got.Jobs[0].Current.StateSeq != 7 || got.Jobs[0].Current.SpecHash != "hash" {
		t.Fatalf("expansion replay: %+v, %v", got, err)
	}
}

func TestEngineBootstrapExchangeReturnsOriginalUnitOnce(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			server, tenant := engineRuntimeStore(t)
			if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCredit, Reference: "launcher-test", CreatedBy: "fixture"}); err != nil {
				t.Fatal(err)
			}
			root := capability.Capability{Prefix: "launcher", Team: "", Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Audience: capability.Controller, Grants: []capability.Grant{capability.ClaimsLaunch}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, ReservedPools: []string{}, Repositories: []string{"gh:1"}, IssuedAt: time.Unix(0, 1).UTC()}
			envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: root})
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte("launcher-secret"))
			engineSQL(t, server.Store, `INSERT INTO capabilities(digest,prefix,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,'launcher','service','launcher','controller',?,'delegate',1,?,'none',?,?,1,'launcher')`, hex.EncodeToString(digest[:]), []byte(`["claims.launch"]`), envelope, []byte(`[]`), []byte(`["gh:1"]`))
			target := &store.UnitRef{Kind: kind, RunID: "run"}
			if kind == capability.UnitPlan {
				engineSQL(t, server.Store, `UPDATE runs SET dispatch='controller'`)
				engineSQL(t, server.Store, `UPDATE triggers SET unit_kind='plan'`)
			} else {
				target.NodeID = "job"
				engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
				engineAcceptedFixturePlan(t, server, "a", "run")
			}
			claim, err := server.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: root.Prefix, Target: target})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := server.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
			if err != nil {
				t.Fatal(err)
			}
			if principal.Capability.Billing == nil || !principal.Capability.Billing.Metered {
				t.Fatal("Cloud bootstrap from an unmetered root lacks metered provenance")
			}
			authorized := capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "forged", RunID: "foreign"}}
			got, err := server.Exchange(t.Context(), authorized)
			if err != nil || got.Claim.Unit != kind || got.Claim.RunID != "run" || got.Claim.JobID != string(target.NodeID) || got.Claim.Purpose != principal.Capability.Purpose || got.Claim.Delivery != capability.DeliveryDirect || got.Claim.Capability == "" || got.Claim.Capability == string(claim.Bearer) || got.Claim.LeaseExpiresAt != claim.Award.LeaseExpiresAt || got.Claim.Runner.Name != "launcher" || got.Claim.Runner.Type != "cloud" {
				t.Fatalf("exchange identity: unit=%s run=%s job=%s delivery=%s, %v", got.Claim.Unit, got.Claim.RunID, got.Claim.JobID, got.Claim.Delivery, err)
			}
			if replay, err := server.Exchange(t.Context(), authorized); err == nil || replay.Claim.Capability != "" {
				t.Fatal("bootstrap returned a second bearer")
			}
		})
	}
}

func TestEngineCancellationIntentPreservesHolderProperty(t *testing.T) {
	property := func(reason string) bool {
		server, tenant := engineRuntimeStore(t)
		owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
		engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
		holder, err := server.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}})
		if err != nil {
			t.Fatal(err)
		}
		before, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil {
			t.Fatal(err)
		}
		first, err := server.cancelJob(t.Context(), owner, controllerwire.CancelRequest{Reason: reason})
		if err != nil || !first.CancelRequested {
			return false
		}
		row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil || row.Status != before.Status || row.Attempt != before.Attempt || row.CapabilityPrefix != holder.Award.CapabilityPrefix || row.ClaimedBy != holder.Award.Holder || row.Generation != holder.Award.Generation || row.CancelReason != reason || row.Outcome != "" || row.FinishedAt != nil {
			return false
		}
		replay, err := server.cancelJob(t.Context(), owner, controllerwire.CancelRequest{Reason: reason})
		return err == nil && replay.Replayed && replay.StateSeq == first.StateSeq
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 32})); err != nil {
		t.Fatal(err)
	}
}
