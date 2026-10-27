package controller_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type githubClaimEnvironment struct{ location string }

func (githubClaimEnvironment) CheckRunner(context.Context, string) error { return nil }

func (env githubClaimEnvironment) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, error) {
	return match.Profile{Location: env.location, OS: "linux", Arch: "amd64"}, nil
}

func configureGitHubClaims(t *testing.T, s *controller.Server, st *store.Store) *controller.Server {
	t.Helper()
	versions, err := engine.NewResolver(engine.Config{BuildVersion: "development", CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s.WithEngineController(controller.NewEngineController(st, controller.EngineController{RunnerPolicy: githubClaimEnvironment{}, VersionPolicy: &controller.SignedEngineVersionPolicy{Resolver: versions}}))
}

func seedSourceRun(t *testing.T, st *store.Store, team, run, remote, key, ref, sha string, appID int64, dispatch runspec.Dispatch, writers ...runspec.Writer) {
	t.Helper()
	now := teststore.Now(st)
	if err := teststore.SeedRun(st, t.Context(), store.Team(team), store.Run{RunSummary: runstate.RunSummary{ID: run, Pipeline: "build", Status: "pending", CreatedAt: now, RepoURL: remote, GitBranch: ref, GitSHA: sha}}); err != nil {
		t.Fatal(err)
	}
	seal := runspec.SealedRequest{Version: 1, RunID: run, AdmittedAt: now, Request: runspec.Request{
		Version: 1, Team: team, Pipeline: "build", Mode: runspec.IntakeExecute, Dispatch: dispatch,
		Git:    runspec.Git{Repository: runspec.Repository{Key: runspec.RepoKey(key), Display: remote}, Ref: ref, SHA: sha},
		Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: remote}},
	}}
	if appID > 0 {
		seal.Request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Repository: "acme/repo", RepositoryID: appID, InstallationID: 7, Ref: ref, SHA: sha, Event: "push", DeliveryKey: "delivery-" + run, VerifiedAt: now, DefaultBranch: "main", DefaultBranchPush: ref == "refs/heads/main"}}
		// The fixture keeps its run's origin, which decides placement, while sealing
		// the verified identity its App facts describe.
		provenance, err := runspec.AppProvenance(seal.Request.Git.App.Value, seal.Request.PullRequest, "build")
		if err != nil {
			t.Fatal(err)
		}
		seal.Provenance = provenance
	} else {
		// Without App facts the run is one the fixture account submitted.
		commit := ""
		if runspec.IsCommitID(sha) {
			commit = sha
		}
		seal.Provenance = controller.AssertedProvenance(t, "build", ref, commit)
	}
	if len(writers) > 0 {
		var err error
		seal.Provenance, err = runspec.AssertedProvenance(writers[0], ref, sha, seal.Request.Pipeline)
		if err != nil {
			t.Fatal(err)
		}
	}
	body := controller.SealWith(t, seal, seal.Provenance)
	if _, err := teststore.Exec(t.Context(), st, `UPDATE runs SET repo_key=?,repo_display=?,request_json=?,dispatch=? WHERE team=? AND id=?`, key, remote, body, dispatch, team, run); err != nil {
		t.Fatal(err)
	}
	controller.EngineAcceptedFixturePlan(t, st, store.Team(team), run, "compile")
	kind := capability.UnitTrigger
	if dispatch == runspec.DispatchController {
		kind = capability.UnitPlan
	}
	if _, err := teststore.Exec(t.Context(), st, `INSERT INTO triggers(team,id,unit_kind,status,created_at,available_at,expires_at,needs_labels,prefers_labels) VALUES (?,?,?,'pending',?,?,?,?,?)`, team, run, kind, now.UnixNano(), now.UnixNano(), now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
}

func githubClaimBody(kind capability.UnitKind, run, job string) controllerwire.ClaimRequest {
	return controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{kind}, RunID: run, JobID: job, Labels: []string{controller.GitHubActionsLabel}}
}

func githubFixtureKey(id int64) string { return fmt.Sprintf("gh:%d", id) }

func TestSourceClaimsFollowSeededReadiness(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			e, _, _ := controller.EngineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
			controller.EngineHTTPRoot(t, e, "", "source-claimant", []capability.Grant{capability.ClaimsLaunch})
			seedSourceRun(t, e.Store, "a", "source-run", "https://github.com/acme/widgets.git", "gh:701", "refs/heads/main", headSHA, 701, runspec.DispatchController)
			target := &store.UnitRef{Kind: kind, RunID: "source-run"}
			if kind == capability.UnitNode {
				if err := teststore.SeedNode(e.Store, t.Context(), store.Node{RunID: "source-run", NodeID: "compile", Status: "pending"}); err != nil {
					t.Fatal(err)
				}
				if err := teststore.SeedNodeReady(e.Store, t.Context(), "source-run", "compile"); err != nil {
					t.Fatal(err)
				}
				target.NodeID = "compile"
			}
			tenant := teststore.ForTeam(t, e.Store, "a")
			if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100000000, Reference: "fixture:source-run", CreatedBy: "fixture"}); err != nil {
				t.Fatal(err)
			}
			claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "source-claimant", Target: target})
			if err != nil {
				t.Fatal(err)
			}
			if claim.Kind != kind || claim.RunID != "source-run" || len(claim.Bearer) == 0 {
				t.Fatalf("claim = %+v", claim)
			}
		})
	}
}
