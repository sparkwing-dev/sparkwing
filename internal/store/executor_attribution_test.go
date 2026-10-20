package store

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestAttemptExecutorComesFromClaimAncestryProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(218, 603))
	for _, hosted := range []bool{false, true} {
		for _, actions := range []bool{false, true} {
			for i := range 12 {
				t.Run(fmt.Sprint(hosted, actions, i), func(t *testing.T) {
					s, demand := unitClaimFixture(t)
					root := authorityRoot("enrolled", "a")
					root.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
					wantKind, wantName, wantLocation := "agent", root.Subject.ID, "local"
					if actions {
						run := fmt.Sprint(random.Uint64())
						root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "main", SHA: fixtureFeatureSHA, WorkflowRunID: run}}
						root.Repositories = []string{"gh:1"}
						writePushRun(t, s, runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: fixtureFeatureSHA})
						rewriteSchemaExec(t, s.db, `INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_by,created_at,repo_key) VALUES ('a',1,7,'acme/widgets','owner',1,'gh:1')`)
						wantKind, wantName, wantLocation = "github-actions", "acme/widgets run "+run, "cloud"
					}
					authorityPersist(t, s, root)
					demand.CapabilityPrefix = root.Prefix
					if hosted {
						host, err := s.ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}})
						if err != nil {
							t.Fatal(err)
						}
						demand.CapabilityPrefix = host.Award.CapabilityPrefix
					}
					claim, err := s.ClaimUnit(t.Context(), demand)
					if err != nil {
						t.Fatal(err)
					}
					actor := Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, Kind: ActorHolder, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
					identity := runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: claim.Award.Generation}
					forged := fmt.Sprintf("cloud:' OR 1=1 --/%x", random.Uint64())
					tenant := &Tenant{s: s, team: "a"}
					_, err = tenant.TransitionNode(t.Context(), NodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, To: NodeRunning, Cause: CauseStart, Actor: actor, SourceAttempt: new(int64), Extra: TransitionExtra{Attempt: &NodeAttempt{Identity: identity, ExecutorKind: forged, ExecutorName: forged, ExecutorLocation: forged}}})
					if err != nil {
						t.Fatal(err)
					}
					view, err := tenant.ReadRunView(t.Context(), "run")
					if err != nil || len(view.Attempts) != 1 {
						t.Fatalf("attempt view: %+v, %v", view.Attempts, err)
					}
					got := view.Attempts[0]
					if got.Identity != identity || got.ExecutorKind != wantKind || got.ExecutorName != wantName || got.ExecutorLocation != wantLocation {
						t.Fatalf("attempt attribution: %+v, want %s/%s/%s", got, wantKind, wantName, wantLocation)
					}
				})
			}
		}
	}
}
