package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ClaimSecurityRegressions runs claim and award trust checks in the supplied dialect.
func ClaimSecurityRegressions(t *testing.T, open func(*testing.T) *Store) {
	fixture := func(t *testing.T) *Store {
		s := open(t)
		s.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1) ON CONFLICT(name) DO NOTHING`)
		s.testOnlyExec(t, `INSERT INTO team_counters(team) VALUES ('a'),('b') ON CONFLICT(team) DO NOTHING`)
		return s
	}
	t.Run("editor asserted main", func(t *testing.T) { actionsClaimRefusesEditorAssertedMain(t, fixture) })
	t.Run("pool trust parity", func(t *testing.T) { reservedPoolTrustClaimAndExplanationParity(t, fixture) })
	t.Run("fresh default branch", func(t *testing.T) { verifiedDefaultRequiresFreshPod(t, fixture) })
	t.Run("fresh pod explanation", func(t *testing.T) { freshPodWaitingExplanation(t, fixture) })
	t.Run("push provenance parity", func(t *testing.T) { pushClaimProvenanceParity(t, fixture) })
	t.Run("pool branch rules", func(t *testing.T) { poolBranchRulesMatchLikePathGlobs(t, fixture) })
}

func poolBranchRulesMatchLikePathGlobs(t *testing.T, open func(*testing.T) *Store) {
	s := open(t)
	for i, branch := range []string{"main", "release/*", "v?", "a_b", "x%y", "*"} {
		s.testOnlyExec(t, `INSERT INTO pool_access_rules(team,id,pool,repo_key,branch,created_by,created_at) VALUES ('a',?,?,'gh:1',?,'owner',1)`, fmt.Sprint(i), branch, branch)
	}
	s.testOnlyExec(t, `INSERT INTO pool_access_rules(team,id,pool,repo_key,source,created_by,created_at) VALUES ('a','app','app-only','gh:1','github_app','owner',1),('a','other','other-repo','gh:2','','owner',1),('b','foreign','foreign','gh:1','','owner',1)`)
	app := func(ref string) runspec.Provenance {
		p, err := runspec.AppProvenance(runspec.AppFacts{Event: "push", RepositoryID: 1, DeliveryKey: "delivery", VerifiedAt: time.Unix(1, 0), Ref: ref, SHA: referenceMain, DefaultBranch: "main", DefaultBranchPush: ref == "refs/heads/main"}, runspec.Optional[runspec.PullRequest]{}, "ci")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	schedule, err := runspec.ScheduleProvenance("nightly", 1, "main", referenceMain, "ci")
	if err != nil {
		t.Fatal(err)
	}
	actions, err := runspec.ActionsProvenance(1, "refs/heads/main", referenceMain, "ci")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		provenance runspec.Provenance
		want       []string
	}{
		{"default branch", app("refs/heads/main"), []string{"*", "app-only", "main"}},
		{"one segment under release", app("refs/heads/release/1"), []string{"app-only", "release/*"}},
		{"two segments under release", app("refs/heads/release/1/2"), []string{"app-only"}},
		{"question mark is one character", app("refs/heads/v1"), []string{"*", "app-only", "v?"}},
		{"underscore is literal", app("refs/heads/aXb"), []string{"*", "app-only"}},
		{"percent is literal", app("refs/heads/xZZy"), []string{"*", "app-only"}},
		{"literal underscore", app("refs/heads/a_b"), []string{"*", "a_b", "app-only"}},
		{"a tag matches no branch rule", app("refs/tags/main"), []string{"app-only"}},
		{"admin-armed schedule is not an App delivery", schedule, []string{"*", "main"}},
		{"actions push gets no pool", actions, []string{}},
		{"asserted run gets no pool", assertedProvenance(t, "ci", "refs/heads/main", referenceMain), []string{}},
	} {
		if got := testAllowedPools(t, s, "a", tc.provenance); !slices.Equal(got, tc.want) {
			t.Errorf("%s: pools=%v want %v", tc.name, got, tc.want)
		}
	}
}

func actionsClaimRefusesEditorAssertedMain(t *testing.T, open func(*testing.T) *Store) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			s := open(t)
			tenant := referenceWorld(t, s, "gh:701")
			editor := referenceMember(t, s, "editor", RoleEditor)
			admitted, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: editor.Authority, IdempotencyKey: "editor-main", Request: runspec.Request{
				Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController,
				Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:701", Display: "https://github.com/acme/app"}, Ref: "refs/heads/main", SHA: referenceMain},
				Source: runspec.Source{Ref: referenceFeature},
			}})
			if err != nil {
				t.Fatal(err)
			}
			s.testOnlyExec(t, `UPDATE triggers SET expires_at=? WHERE id=?`, s.now().Add(time.Hour).UnixNano(), admitted.RunID)
			root := authorityRoot("actions", "a")
			root.Grants = []capability.Grant{capability.TriggersClaim}
			root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:701", Ref: "refs/heads/main", SHA: referenceMain, WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/app/.github/workflows/sparkwing.yaml@refs/heads/main"}}
			authorityPersist(t, s, root)
			demand := UnitDemand{CapabilityPrefix: root.Prefix, Kinds: []capability.UnitKind{capability.UnitPlan}}
			if named {
				demand.Target = &UnitRef{Kind: capability.UnitPlan, RunID: runspec.RunID(admitted.RunID)}
			}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if named && !refusal(err) || !named && !errors.Is(err, ErrNotFound) {
				t.Fatalf("Actions runner claimed editor's asserted main: %+v %v", claim, err)
			}
		})
	}
}

func reservedPoolTrustClaimAndExplanationParity(t *testing.T, open func(*testing.T) *Store) {
	for _, trust := range []capability.Trust{"", capability.TrustAny, capability.TrustVerifiedDefault} {
		for _, secret := range []bool{false, true} {
			for _, launcher := range []bool{false, true} {
				for _, verified := range []bool{false, true} {
					t.Run(fmt.Sprint(trust, secret, launcher, verified), func(t *testing.T) {
						s, demand := unitClaimFixtureStore(t, open(t))
						root := authorityRoot("pool", "a")
						root.Trust, root.ReservedPools = trust, []string{"prod"}
						root.Grants = []capability.Grant{capability.NodesClaim}
						if launcher {
							root.Grants = []capability.Grant{capability.ClaimsLaunch}
						}
						authorityPersist(t, s, root)
						demand.CapabilityPrefix = root.Prefix
						s.testOnlyExec(t, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"value":["pool=prod"]}}`))
						if secret {
							s.testOnlyExec(t, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,pipeline,shared,only_on) VALUES ('a','DEPLOY','ciphertext','account',1,1,1,'',0,'prod')`)
						}
						branch := "feature"
						if verified {
							branch = "main"
						}
						grantFixturePool(t, s, "run", "prod", branch)
						indexFixtureUnit(t, s, "a", *demand.Target)
						want := (!secret || launcher) && (trust == capability.TrustAny || trust == "" && !secret || verified && launcher)
						err := s.inTx(t.Context(), func(tx *storeTx) error {
							explained, err := explainUnitPlacementTx(t.Context(), tx, "a", *demand.Target, demand)
							if err != nil {
								return err
							}
							if explained.ok() != want {
								t.Errorf("explainer eligible=%v, want %v (%v)", explained.ok(), want, explained.refused)
							}
							return nil
						})
						if err != nil {
							t.Fatal(err)
						}
						_, err = s.AsOperator().ClaimUnit(t.Context(), demand)
						if (err == nil) != want {
							t.Fatalf("claim error=%v, want eligible=%v", err, want)
						}
					})
				}
			}
		}
	}
}

func pushClaimProvenanceParity(t *testing.T, open func(*testing.T) *Store) {
	for _, tc := range []struct {
		trust   runspec.TrustClass
		repo    int64
		allowed bool
	}{
		{runspec.TrustActions, 1, true},
		{runspec.TrustVerified, 1, true},
		{runspec.TrustVerifiedDefault, 1, true},
		{runspec.TrustActions, 2, false},
		{runspec.TrustSchedule, 1, false},
		{runspec.TrustAsserted, 0, false},
	} {
		t.Run(fmt.Sprint(tc.trust, tc.repo), func(t *testing.T) {
			s, _ := unitClaimFixtureStore(t, open(t))
			root := authorityRoot("actions", "a")
			root.Grants = []capability.Grant{capability.TriggersClaim}
			root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "refs/heads/main", SHA: referenceMain, WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
			authorityPersist(t, s, root)
			var p runspec.Provenance
			var err error
			switch tc.trust {
			case runspec.TrustActions:
				p, err = runspec.ActionsProvenance(tc.repo, "refs/heads/main", referenceMain, "ci")
			case runspec.TrustVerified, runspec.TrustVerifiedDefault:
				p, err = runspec.AppProvenance(runspec.AppFacts{Event: "push", RepositoryID: tc.repo, DeliveryKey: "delivery", VerifiedAt: s.now(), Ref: "refs/heads/main", SHA: referenceMain, DefaultBranch: "main", DefaultBranchPush: tc.trust == runspec.TrustVerifiedDefault}, runspec.Optional[runspec.PullRequest]{}, "ci")
			case runspec.TrustSchedule:
				p, err = runspec.ScheduleProvenance("schedule", tc.repo, "main", referenceMain, "ci")
			case runspec.TrustAsserted:
				p = assertedProvenance(t, "ci", "refs/heads/main", referenceMain)
			}
			if err != nil {
				t.Fatal(err)
			}
			body := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "refs/heads/main", SHA: referenceMain}}}, p)
			s.testOnlyExec(t, `UPDATE runs SET request_json=? WHERE id='run'`, body)
			err = s.inTx(t.Context(), func(tx *storeTx) error {
				_, err := awardCapabilityTx(t.Context(), tx, capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitTrigger, UnitID: "run", Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect}, &awardRow{team: "a", run: "run", holder: "actions", generation: 1, lease: sql.NullInt64{Valid: true, Int64: s.now().Add(time.Hour).UnixNano()}})
				return err
			})
			if (err == nil) != tc.allowed {
				t.Fatalf("direct award error=%v, want allowed=%v", err, tc.allowed)
			}
			_, err = s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Kinds: []capability.UnitKind{capability.UnitTrigger}})
			if (err == nil) != tc.allowed {
				t.Fatalf("queued claim error=%v, want allowed=%v", err, tc.allowed)
			}
		})
	}
}

func verifiedDefaultRequiresFreshPod(t *testing.T, open func(*testing.T) *Store) {
	const why = "verified default-branch work on Kubernetes runs only on fresh pods; add a launcher pool or use Sparkwing Cloud"
	for _, source := range []string{"verified-default", "verified-main", "schedule", "inherited-schedule", "verified-feature", "asserted-main"} {
		for _, runner := range []string{"plain-machine", "persistent", "launcher", "cloud", "actions"} {
			for _, named := range []bool{false, true} {
				t.Run(fmt.Sprint(source, "/", runner, "/", named), func(t *testing.T) {
					s, demand := unitClaimFixtureStore(t, open(t))
					ctx := t.Context()
					ref := *demand.Target
					seal := runspec.SealedRequest{Request: runspec.Request{Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}}
					branch := "main"
					if source == "verified-feature" {
						branch = "feature"
					}
					seal.Provenance = assertedProvenance(t, "ci", "refs/heads/main", referenceMain)
					if source == "schedule" || source == "inherited-schedule" {
						var err error
						seal.Provenance, err = runspec.ScheduleProvenance("nightly", 1, "main", referenceMain, "ci")
						if err != nil {
							t.Fatal(err)
						}
						if source == "inherited-schedule" {
							seal.Request.Lineage.ParentRunID = "parent"
							seal.Request.Pipeline = "child"
						}
					} else if source != "asserted-main" {
						var err error
						seal.Provenance, err = runspec.AppProvenance(runspec.AppFacts{Event: "push", RepositoryID: 1, DeliveryKey: "delivery", VerifiedAt: s.now(), Ref: "refs/heads/" + branch, SHA: referenceMain, DefaultBranch: "main", DefaultBranchPush: source == "verified-default"}, runspec.Optional[runspec.PullRequest]{}, "ci")
						if err != nil {
							t.Fatal(err)
						}
					}
					body, err := json.Marshal(seal)
					if err != nil {
						t.Fatal(err)
					}
					s.testOnlyExec(t, `UPDATE runs SET request_json=?,dispatch='controller' WHERE id='run'`, body)
					s.testOnlyExec(t, `UPDATE triggers SET status='done',finished_at=1 WHERE id='run'`)
					root := authorityRoot(runner, "a")
					root.Trust = capability.TrustAny
					if runner == "launcher" {
						root.Grants, root.ReservedPools = []capability.Grant{capability.ClaimsLaunch}, []string{"prod"}
						s.testOnlyExec(t, `INSERT INTO pool_access_rules(team,id,pool,repo_key,created_by,created_at) VALUES ('a','prod','prod','gh:1','owner',1)`)
						s.testOnlyExec(t, `UPDATE nodes SET execution_json=? WHERE run_id='run'`, []byte(`{"requires":{"value":["pool=prod","team-4cpu"]}}`))
					}
					if runner == "cloud" {
						root.Team, root.Subject.Kind, root.Grants = "", capability.SubjectService, []capability.Grant{capability.ClaimsLaunch}
						s.testOnlyExec(t, `UPDATE nodes SET execution_json=? WHERE run_id='run'`, []byte(`{"requires":{"value":["sparkwing-4cpu"]}}`))
						fundUnitClaim(t, s)
					}
					if runner == "actions" {
						root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "refs/heads/" + branch, SHA: referenceMain, WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/" + branch}}
					}
					if runner == "persistent" || runner == "plain-machine" || runner == "launcher" {
						owner := referenceMember(t, s, "owner", RoleOwner)
						platform := "kubernetes"
						if runner == "plain-machine" {
							platform = ""
						}
						issued, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &owner.Authority, RegistrationID: "runner:a:" + runner, Team: "a", Subject: root.Subject, Grants: root.Grants, Binding: root.Binding, Repositories: []string{"github.com/acme/*"}, ReservedPools: root.ReservedPools, Trust: root.Trust, Platform: platform})
						if err != nil {
							t.Fatal(err)
						}
						root = issued.Capability
					} else {
						authorityPersist(t, s, root)
					}
					demand.CapabilityPrefix = root.Prefix
					// Advertisements cannot turn a persistent enrollment into a fresh pod.
					demand.Labels = append(demand.Labels, "single-use", "launcher", "class=cloud")
					indexFixtureUnit(t, s, "a", ref)
					want := runner != "persistent" || source == "asserted-main" || source == "verified-feature"
					if runner == "launcher" && source == "asserted-main" {
						want = false
					}
					if runner == "actions" && (source == "asserted-main" || source == "schedule" || source == "inherited-schedule") {
						want = false
					}
					if runner == "persistent" && !want {
						if err := s.AsOperator().RecordRunnerPoll(ctx, demand); err != nil {
							t.Fatal(err)
						}
						view, err := (&Tenant{s: s, team: "a"}).ReadRunView(ctx, "run")
						if err != nil || view.Waiting == nil || view.Waiting.NoEligible != why {
							t.Errorf("waiting explanation = %+v, %v", view.Waiting, err)
						}
						unitID, err := encodeAwardUnit(ref)
						if err != nil {
							t.Fatal(err)
						}
						err = s.inTx(ctx, func(tx *storeTx) error {
							_, err := awardCapabilityTx(ctx, tx, capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitNode, UnitID: unitID, Purpose: capability.PurposeWork, Delivery: capability.DeliveryDirect}, &awardRow{team: "a", run: "run", node: "job", holder: runner, generation: 1, lease: sql.NullInt64{Valid: true, Int64: s.now().Add(time.Minute).UnixNano()}})
							return err
						})
						if !refusal(err) || err.Error() != why {
							t.Errorf("direct award refusal = %v", err)
						}
					}
					if !named {
						demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
					}
					claim, err := s.AsOperator().ClaimUnit(ctx, demand)
					if (err == nil) != want {
						t.Fatalf("claimed run = %q, %v; want eligible=%v", claim.RunID, err, want)
					}
					if !want && named && (!refusal(err) || runner == "persistent" && err.Error() != why) {
						t.Fatalf("named refusal = %v", err)
					}
					if !want && !named && !errors.Is(err, ErrNotFound) {
						t.Fatalf("queued refusal = %v", err)
					}
				})
			}
		}
	}
}

func freshPodWaitingExplanation(t *testing.T, open func(*testing.T) *Store) {
	for _, dispatch := range []runspec.Dispatch{runspec.DispatchController, runspec.DispatchHost} {
		for _, selection := range []runspec.RunnerSelection{{Kind: runspec.RunnerName, Value: "persistent"}, {Kind: runspec.RunnerLabel, Value: "gpu"}} {
			for _, mismatch := range []string{"", "repository", "selector", "tier"} {
				if mismatch == "tier" && dispatch == runspec.DispatchHost {
					continue
				}
				t.Run(fmt.Sprint(dispatch, "/", selection.Kind, "/", mismatch), func(t *testing.T) {
					s, demand := unitClaimFixtureStore(t, open(t))
					ctx := t.Context()
					if mismatch != "" {
						ctx = WithClaimPlacement(ctx, ClaimPlacement{Liveness: time.Minute})
					}
					p, err := runspec.AppProvenance(runspec.AppFacts{Event: "push", RepositoryID: 1, DeliveryKey: "delivery", VerifiedAt: s.now(), Ref: "refs/heads/main", SHA: referenceMain, DefaultBranch: "main", DefaultBranchPush: true}, runspec.Optional[runspec.PullRequest]{}, "ci")
					if err != nil {
						t.Fatal(err)
					}
					request := runspec.Request{Mode: runspec.IntakeExecute, Dispatch: dispatch, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}
					request.Options.Runner.Optional = runspec.Optional[runspec.RunnerSelection]{Set: true, Value: selection}
					if mismatch == "tier" {
						request.Options.Runner.Optional = runspec.Optional[runspec.RunnerSelection]{}
						request.Dispatch = runspec.DispatchController
						s.testOnlyExec(t, `UPDATE teams SET claim_ranking=? WHERE name='a'`, `["cloud"]`)
					}
					body := sealWith(t, runspec.SealedRequest{Request: request}, p)
					s.testOnlyExec(t, `UPDATE runs SET request_json=?,dispatch=? WHERE id='run'`, body, request.Dispatch)
					ref := *demand.Target
					if dispatch == runspec.DispatchHost {
						ref = UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
					} else {
						s.testOnlyExec(t, `UPDATE triggers SET status='done',finished_at=1 WHERE id='run'`)
					}
					owner := referenceMember(t, s, "owner", RoleOwner)
					repos := []string{"github.com/acme/*"}
					if mismatch == "repository" {
						repos = []string{"github.com/other/*"}
					}
					issued, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &owner.Authority, RegistrationID: "runner:a:persistent", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "persistent"}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: repos, Trust: capability.TrustAny, Platform: "kubernetes"})
					if err != nil {
						t.Fatal(err)
					}
					demand.CapabilityPrefix, demand.Target = issued.Capability.Prefix, &ref
					demand.Labels = append(demand.Labels, "gpu")
					if mismatch == "selector" {
						if selection.Kind == runspec.RunnerName {
							request.Options.Runner.Value.Value = "another-runner"
							s.testOnlyExec(t, `UPDATE runs SET request_json=? WHERE id='run'`, sealWith(t, runspec.SealedRequest{Request: request}, p))
						} else {
							demand.Labels = []string{"team-4cpu"}
						}
					}
					indexFixtureUnit(t, s, "a", ref)
					if err := s.AsOperator().RecordRunnerPoll(ctx, demand); err != nil {
						t.Fatal(err)
					}
					view, err := (&Tenant{s: s, team: "a"}).ReadRunView(ctx, "run")
					if err != nil || view.Waiting == nil {
						t.Fatalf("waiting unit = %+v, %v", view.Waiting, err)
					}
					want := "verified default-branch work on Kubernetes runs only on fresh pods; add a launcher pool or use Sparkwing Cloud"
					if dispatch == runspec.DispatchHost {
						want = "verified default-branch work on Kubernetes runs only on fresh pods; change the run's runner selection to a launcher pool or Sparkwing Cloud and start a new run"
					}
					if got := view.Waiting.NoEligible == want; got != (mismatch == "") {
						t.Fatalf("waiting explanation = %q, mismatch=%q", view.Waiting.NoEligible, mismatch)
					}
				})
			}
		}
	}
}
