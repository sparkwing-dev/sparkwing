package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestPushClaimsCannotEscapeSealedScopeProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(76, 321))
	for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitTrigger} {
		for _, named := range []bool{false, true} {
			for _, descendant := range []bool{false, true} {
				for _, field := range []string{"repo", "ref", "sha"} {
					t.Run(fmt.Sprint(kind, named, descendant, field), func(t *testing.T) {
						s, demand := unitClaimFixture(t)
						root := authorityRoot("actions", "a")
						root.Grants = []capability.Grant{capability.TriggersClaim, capability.NodesClaim}
						root.Repositories = []string{"gh:1"}
						root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "main", SHA: fixtureFeatureSHA, WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
						authorityPersist(t, s, root)
						demand.CapabilityPrefix = root.Prefix
						if descendant {
							child := authorityRoot("actions-child", "a")
							child.Grants, child.Repositories, child.Binding = root.Grants, root.Repositories, root.Binding
							child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
							authorityPersist(t, s, child)
							demand.CapabilityPrefix = child.Prefix
						}
						unit := UnitRef{Kind: kind, RunID: "run"}
						if kind == capability.UnitNode {
							unit.NodeID = "job"
						}
						demand.Target, demand.Kinds = nil, []capability.UnitKind{kind}
						if named {
							demand.Target = &unit
						}
						git := runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: fixtureFeatureSHA}
						altered := git
						value := fmt.Sprintf("adversarial-%016x:' OR 1=1 --/雪", random.Uint64())
						switch field {
						case "repo":
							altered.Repository.Key = runspec.RepoKey(value)
						case "ref":
							altered.Ref = value
						case "sha":
							// A sealed commit is always a full commit ID.
							altered.SHA = fmt.Sprintf("%040x", random.Uint64())
						}
						writePushRun(t, s, altered)
						_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
						var refused *apierr.Error
						if named && (!errors.As(err, &refused) || refused.Code != apierr.Forbidden) || !named && !errors.Is(err, ErrNotFound) {
							t.Fatalf("out-of-scope claim: %v", err)
						}
						rewriteSchemaCount(t, s.db, "billing_awards", 0)
						rewriteSchemaCount(t, s.db, "events", 0)
						writePushRun(t, s, git)
						claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
						if err != nil || claim.Kind != kind || claim.RunID != "run" {
							t.Fatalf("matching push claim: %+v, %v", claim, err)
						}
					})
				}
			}
		}
	}
}

func writePushRun(t *testing.T, s *Store, git runspec.Git) {
	t.Helper()
	provenance, err := runspec.ActionsProvenance(1, "acme/repo", git.Ref, git.SHA, "ci")
	if err != nil {
		t.Fatal(err)
	}
	body := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Git: git}}, provenance)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,repo_key=?,git_branch=?,git_sha=? WHERE id='run'`, body, git.Repository.Key, git.Ref, git.SHA)
}

func TestPushAwardRefusesScopeEscape(t *testing.T) {
	s, _ := unitClaimFixture(t)
	root := authorityRoot("actions", "a")
	root.Grants = []capability.Grant{capability.TriggersClaim}
	root.Repositories = []string{"gh:1"}
	root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "main", SHA: fixtureFeatureSHA, WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
	authorityPersist(t, s, root)
	writePushRun(t, s, runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: fixtureOtherSHA})
	seedTriggerClaim(t, s, "run", "actions", "actions", 1, s.now().Add(time.Hour))
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := s.issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitTrigger, UnitID: "run", Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect}})
		return err
	})
	if err == nil {
		t.Fatal("direct award escaped the Actions push")
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 0)
}

func TestPushAwardedHostRetainsAncestorScopeProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(436, 182))
	for range 16 {
		s, demand := unitClaimFixture(t)
		root := authorityRoot("actions", "a")
		root.Grants = []capability.Grant{capability.TriggersClaim, capability.NodesClaim}
		root.Repositories = []string{"gh:1"}
		root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "main", SHA: fixtureFeatureSHA, WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
		authorityPersist(t, s, root)
		git := runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: fixtureFeatureSHA}
		writePushRun(t, s, git)
		host, err := s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}})
		if err != nil {
			t.Fatal(err)
		}
		demand.CapabilityPrefix = host.Award.CapabilityPrefix
		altered := git
		altered.SHA = fmt.Sprintf("%040x", random.Uint64())
		writePushRun(t, s, altered)
		_, err = s.AsOperator().ClaimUnit(t.Context(), demand)
		var refused *apierr.Error
		if !errors.As(err, &refused) || refused.Code != apierr.Forbidden {
			t.Fatalf("awarded host escaped its push ancestor: %v", err)
		}
		rewriteSchemaCount(t, s.db, "billing_awards", 1)
		writePushRun(t, s, git)
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil || claim.NodeID != "job" {
			t.Fatalf("awarded host in scope: %+v, %v", claim, err)
		}
	}
}
