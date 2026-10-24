package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	t.Run("push provenance parity", func(t *testing.T) { pushClaimProvenanceParity(t, fixture) })
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
						s.testOnlyExec(t, `UPDATE runs SET allowed_pools=?`, []byte(`["prod"]`))
						s.testOnlyExec(t, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"value":["pool=prod"]}}`))
						if secret {
							s.testOnlyExec(t, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,pipeline,shared,only_on) VALUES ('a','DEPLOY','ciphertext','account',1,1,1,'',0,'prod')`)
						}
						if verified {
							p, err := runspec.AppProvenance(runspec.AppFacts{Event: "push", RepositoryID: 1, DeliveryKey: "verified", VerifiedAt: s.now(), Ref: "refs/heads/main", SHA: referenceMain, DefaultBranch: "main", DefaultBranchPush: true}, runspec.Optional[runspec.PullRequest]{}, "ci")
							if err != nil {
								t.Fatal(err)
							}
							body := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}}}, p)
							s.testOnlyExec(t, `UPDATE runs SET request_json=?`, body)
						}
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
	for _, source := range []string{"verified-default", "verified-main", "verified-feature", "asserted-main"} {
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
					if source != "asserted-main" {
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
						s.testOnlyExec(t, `UPDATE runs SET allowed_pools=? WHERE id='run'`, []byte(`["prod"]`))
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
					if runner == "actions" && source == "asserted-main" {
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
