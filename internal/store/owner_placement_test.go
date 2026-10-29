package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func ownerPlacementFixture(t *testing.T) (*Tenant, runstate.Actor) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{}`))
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	return &Tenant{s: s, team: "a"}, runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
}

func TestOwnerSnapshotPlanAndEvalUseTheirOwnLease(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			fundUnitClaim(t, s)
			root := authorityRoot("launcher", "")
			root.Subject.Kind, root.Grants = capability.SubjectService, []capability.Grant{capability.ClaimsLaunch}
			authorityPersist(t, s, root)
			demand.CapabilityPrefix, demand.Target = root.Prefix, &UnitRef{Kind: kind, RunID: "run"}
			rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
			if kind == capability.UnitEval {
				demand.Target.NodeID, demand.Target.SourceGeneration = "job", 7
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
				rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','job',7,'pending')`)
			}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			tenant := &Tenant{s: s, team: "a"}
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}
			snapshot, err := tenant.ReadOwnerSnapshot(t.Context(), ref, "run")
			if err != nil || snapshot.Run.ID != "run" || len(snapshot.Nodes) != 1 || OwnerCanExecute(capability.Principal{Authority: ref, Capability: issued.Capability})(snapshot.Nodes[0]) {
				t.Fatalf("private %s snapshot run=%s nodes=%d: %v", kind, snapshot.Run.ID, len(snapshot.Nodes), err)
			}
			if kind == capability.UnitEval {
				rewriteSchemaExec(t, s.db, `UPDATE eval_units SET generation=generation+1`)
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET generation=generation+1`)
			}
			if _, err := tenant.ReadOwnerSnapshot(t.Context(), ref, "run"); err == nil {
				t.Fatal("stale owner read private execution state")
			}
		})
	}
}

func TestOwnerSnapshotRechecksLabelsAndLivePreferences(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.3s measured in the short class; the full suite runs it")
	}
	for _, change := range []string{"labels", "preferred", "stale_preferred", "revoked_preferred"} {
		t.Run(change, func(t *testing.T) {
			tenant, actor := ownerPlacementFixture(t)
			s := tenant.s
			s.claimProfiles = claimProfileFake{profile: match.Profile{}}
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_hash='declaration',ready_at=?`, s.now().UnixNano())
			ctx := WithClaimPlacement(t.Context(), ClaimPlacement{Preferences: []PlacementPreference{{Prefers: []string{"name=preferred"}, Hold: time.Hour}, {}}, Liveness: time.Minute})
			owner, err := s.AsOperator().ResolveAuthority(ctx, actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, "run")
			if err != nil || !OwnerCanExecute(owner)(snapshot.Nodes[0]) {
				t.Fatalf("initial owner eligibility: %v", err)
			}
			if change == "labels" {
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"value":["gpu"]}}`))
				indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
				s.claimProfiles = claimProfileFake{profile: match.Profile{}}
			} else {
				preferred := authorityRoot("preferred", "a")
				authorityPersist(t, s, preferred)
				if err := s.AsOperator().RecordRunnerPoll(ctx, UnitDemand{CapabilityPrefix: preferred.Prefix}); err != nil {
					t.Fatal(err)
				}
				if change == "stale_preferred" {
					rewriteSchemaExec(t, s.db, `UPDATE capabilities SET claim_observation_json=? WHERE prefix='preferred'`, []byte(`{"observed_at":"2000-01-01T00:00:00Z"}`))
				} else if change == "revoked_preferred" {
					member := authorityMember(t, s, "owner", RoleOwner)
					if err := tenant.RevokeAuthority(ctx, member, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: preferred.Prefix}); err != nil {
						t.Fatal(err)
					}
				}
			}
			updated, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, "run")
			want := change == "stale_preferred" || change == "revoked_preferred"
			if err != nil || OwnerCanExecute(owner)(updated.Nodes[0]) != want {
				t.Fatalf("current eligibility want=%v: %v", want, err)
			}
			result, err := tenant.ReconcileNode(ctx, ReconcileRequest{Ref: snapshot.Nodes[0].Ref, Actor: actor, Policy: runstate.SourceNone})
			kind := runstate.Dispatch
			if want {
				kind = runstate.Execute
			}
			if err != nil || result.Kind != kind {
				t.Fatalf("transactional stale projection recheck: %s want=%s %v", result.Kind, kind, err)
			}
		})
	}
}

func TestOwnerSnapshotUsesOnlyAuthenticatedAncestorPollForRemoteSelfClaim(t *testing.T) {
	for _, observation := range []string{"live", "missing", "stale", "future", "foreign"} {
		t.Run(observation, func(t *testing.T) {
			tenant, actor := ownerPlacementFixture(t)
			s := tenant.s
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_hash='declaration'`)
			prefix := "runner"
			if observation == "foreign" {
				root := authorityRoot("other", "a")
				authorityPersist(t, s, root)
				prefix = "other"
			}
			if observation != "missing" {
				if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: prefix}); err != nil {
					t.Fatal(err)
				}
			}
			if observation == "stale" {
				raw, err := json.Marshal(runnerPoll{UnitDemand: UnitDemand{}, ObservedAt: s.now().Add(-time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET claim_observation_json=? WHERE prefix='runner'`, raw)
			} else if observation == "future" {
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET claim_observation_json=? WHERE prefix='runner'`, []byte(`{"observed_at":"2100-01-01T00:00:00Z"}`))
			}
			ctx := WithClaimPlacement(t.Context(), ClaimPlacement{Liveness: time.Minute})
			owner, err := s.AsOperator().ResolveAuthority(ctx, actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, "run")
			if err != nil || OwnerCanExecute(owner)(snapshot.Nodes[0]) != (observation == "live" || observation == "stale") {
				t.Fatalf("remote observation %s: %v", observation, err)
			}
			result, err := tenant.ReconcileNode(ctx, ReconcileRequest{Ref: snapshot.Nodes[0].Ref, Actor: actor, Policy: runstate.SourceNone})
			want := runstate.Dispatch
			if observation == "live" || observation == "stale" {
				want = runstate.Execute
			}
			if err != nil || result.Kind != want {
				t.Fatalf("remote selfclaim=%s want=%s: %v", result.Kind, want, err)
			}
		})
	}
}

func TestLiveOwnerKeepsCapturedPlacementWhileRootDoesNotPoll(t *testing.T) {
	s, demand := unitClaimFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_hash='declaration'`)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["team-4cpu","os=linux","arch=amd64"]}}`))
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	slots := 1
	demand.FreeSlots = &slots
	demand.OS, demand.Arch = "linux", "amd64"
	demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitTrigger}
	if err := s.AsOperator().RecordRunnerPoll(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	var body []byte
	if err := s.queryRow(t.Context(), `SELECT claim_observation_json FROM capabilities WHERE team='a' AND prefix='runner'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var poll runnerPoll
	if err := json.Unmarshal(body, &poll); err != nil {
		t.Fatal(err)
	}
	if poll.FreeSlots == nil || *poll.FreeSlots != 0 {
		t.Fatalf("trigger did not consume the queue slot: %+v", poll.FreeSlots)
	}
	ctx := WithClaimPlacement(t.Context(), ClaimPlacement{Liveness: time.Second})
	for range 4 {
		now = now.Add(time.Minute)
		if _, err := tenant.RenewOwner(ctx, actor); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := s.AsOperator().ResolveAuthority(ctx, actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, "run")
	if err != nil || !OwnerCanExecute(owner)(snapshot.Nodes[0]) {
		t.Fatalf("live owner lost its captured placement: %v", err)
	}
	result, err := tenant.ReconcileNode(ctx, ReconcileRequest{Ref: snapshot.Nodes[0].Ref, Actor: actor, Policy: runstate.SourceNone})
	if err != nil || result.Kind != runstate.Execute {
		t.Fatalf("owner did not execute its job after consuming the queue slot: %s %v", result.Kind, err)
	}
	now = now.Add(time.Hour)
	if _, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, "run"); err == nil {
		t.Fatal("expired owner read private placement")
	}
}

func TestOwnerSnapshotSelfExecutionUsesCanonicalMatcherProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.1s measured in the short class; the full suite runs it")
	}
	random := rand.New(rand.NewPCG(971, 382))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tenant, actor := ownerPlacementFixture(t)
			capacity := fmt.Sprintf("size-%d", random.IntN(4))
			requested := fmt.Sprintf("size-%d", random.IntN(4))
			execution := runspec.JobExecutionSettings{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"tool:git", requested}}}}
			raw, err := json.Marshal(execution)
			if err != nil {
				t.Fatal(err)
			}
			tenant.s.claimProfiles = claimProfileFake{profile: match.Profile{Labels: []string{"tool:git", capacity}}}
			rewriteSchemaExec(t, tenant.s.db, `DELETE FROM node_attempts`)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,started_at=NULL,ready_at=1,spec_hash='declaration',execution_json=?`, raw)
			indexFixtureUnit(t, tenant.s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
			owner, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			public, err := tenant.ReadSnapshot(t.Context(), "run")
			if err != nil || OwnerCanExecute(owner)(public.Nodes[0]) {
				t.Fatalf("unprojected snapshot authorized execution: %v", err)
			}
			snapshot, err := tenant.ReadOwnerSnapshot(t.Context(), actor.Authority, "run")
			if err != nil {
				t.Fatal(err)
			}
			var transported RunSnapshot
			body, err := json.Marshal(snapshot)
			if err != nil || json.Unmarshal(body, &transported) != nil {
				t.Fatal("private snapshot did not survive transport")
			}
			node := transported.Nodes[0]
			if got := OwnerCanExecute(owner)(node); got != (requested == capacity) {
				t.Fatalf("requested=%s capacity=%s eligible=%v", requested, capacity, got)
			}
			if requested == capacity {
				for _, mutate := range []func(*runstate.NodeRow){func(n *runstate.NodeRow) { n.Generation++ }, func(n *runstate.NodeRow) { n.SpecHash += "changed" }, func(n *runstate.NodeRow) { n.Ref.NodeID += "other" }} {
					forged := node
					mutate(&forged)
					if OwnerCanExecute(owner)(forged) {
						t.Fatal("stale declaration or generation authorized execution")
					}
				}
				owner.Capability.Binding.Generation++
				if OwnerCanExecute(owner)(node) {
					t.Fatal("foreign owner generation authorized execution")
				}
			}
			result, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: node.Ref, Actor: actor, Policy: runstate.SourceNone})
			want := runstate.Execute
			if requested != capacity {
				want = runstate.Dispatch
			}
			if err != nil || result.Kind != want {
				t.Fatalf("recheck=%s want=%s error=%v", result.Kind, want, err)
			}
		})
	}
}

func TestOwnerNodeReadsMatchTheSnapshotPlacementDecision(t *testing.T) {
	tenant, actor := ownerPlacementFixture(t)
	s := tenant.s
	s.claimProfiles = claimProfileFake{profile: match.Profile{}}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_hash='declaration',ready_at=?`, s.now().UnixNano())
	owner, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadOwnerSnapshot(t.Context(), actor.Authority, "run")
	if err != nil || len(snapshot.Nodes) == 0 || !OwnerCanExecute(owner)(snapshot.Nodes[0]) {
		t.Fatalf("snapshot eligibility: %v", err)
	}
	node, err := tenant.ReadOwnerNode(t.Context(), actor.Authority, snapshot.Nodes[0].Ref)
	if err != nil || !OwnerCanExecute(owner)(node) {
		t.Fatalf("one-node read lost the placement decision: %+v %v", node.OwnerExecution, err)
	}
	nodes, err := tenant.ReadOwnerNodes(t.Context(), actor.Authority, "run")
	if err != nil || len(nodes) != len(snapshot.Nodes) || nodes[0].OwnerExecution != nil {
		t.Fatalf("authority read computed placement decisions: %+v %v", nodes, err)
	}
	if _, err := tenant.ReadOwnerNode(t.Context(), actor.Authority, runspec.NodeRef{RunID: "run", NodeID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node: %v", err)
	}
}

func TestOwnerNodeReadsRefuseWhatTheSnapshotRefuses(t *testing.T) {
	read := map[string]func(*Tenant, capability.AuthorityRef) error{
		"nodes": func(tenant *Tenant, ref capability.AuthorityRef) error {
			_, err := tenant.ReadOwnerNodes(t.Context(), ref, "run")
			return err
		},
		"node": func(tenant *Tenant, ref capability.AuthorityRef) error {
			_, err := tenant.ReadOwnerNode(t.Context(), ref, runspec.NodeRef{RunID: "run", NodeID: "job"})
			return err
		},
	}
	for name, readNodes := range read {
		for _, change := range []string{"other_owner", "superseded_owner", "revoked_owner"} {
			t.Run(name+"/"+change, func(t *testing.T) {
				tenant, actor := ownerPlacementFixture(t)
				s := tenant.s
				ref := actor.Authority
				switch change {
				case "other_owner":
					other := authorityRoot("other-owner", "a")
					authorityPersist(t, s, other)
					ref = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: other.Prefix}
				case "superseded_owner":
					rewriteSchemaExec(t, s.db, `UPDATE triggers SET generation=generation+1`)
				case "revoked_owner":
					member := authorityMember(t, s, "owner", RoleOwner)
					if err := tenant.RevokeAuthority(t.Context(), member, ref); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := tenant.ReadOwnerSnapshot(t.Context(), ref, "run"); err == nil {
					t.Fatal("fixture change did not refuse the full snapshot")
				}
				if err := readNodes(tenant, ref); err == nil {
					t.Fatalf("%s read accepted a refused owner", name)
				}
			})
		}
	}
}

func TestOwnerNodeReadKeepsARefusedPlacementDecision(t *testing.T) {
	tenant, actor := ownerPlacementFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"value":["gpu"]}}`))
	indexFixtureUnit(t, tenant.s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	s := tenant.s
	s.claimProfiles = claimProfileFake{profile: match.Profile{}}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_hash='declaration',ready_at=?`, s.now().UnixNano())
	owner, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadOwnerSnapshot(t.Context(), actor.Authority, "run")
	if err != nil || len(snapshot.Nodes) == 0 || snapshot.Nodes[0].OwnerExecution == nil || OwnerCanExecute(owner)(snapshot.Nodes[0]) {
		t.Fatalf("fixture did not refuse placement: %v", err)
	}
	node, err := tenant.ReadOwnerNode(t.Context(), actor.Authority, snapshot.Nodes[0].Ref)
	if err != nil || node.OwnerExecution == nil || OwnerCanExecute(owner)(node) {
		t.Fatalf("one-node read changed a refused placement decision: %+v %v", node.OwnerExecution, err)
	}
}
