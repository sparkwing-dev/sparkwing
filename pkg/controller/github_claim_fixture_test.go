package controller_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type githubClaimEnvironment struct{ location string }

func (githubClaimEnvironment) CheckRunner(context.Context, string) error { return nil }

func (env githubClaimEnvironment) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	capacity := match.Resources{Cores: 8, MemoryBytes: 8 << 30}
	return match.Profile{Location: env.location, Capacity: capacity, OS: "linux", Arch: "amd64"}, capacity, nil
}

func configureGitHubClaims(s *controller.Server, st *store.Store) *controller.Server {
	st.SetClaimProfileResolver(githubClaimEnvironment{})
	return s.WithEngineController(&controller.EngineController{Store: st, RunnerPolicy: githubClaimEnvironment{}})
}

func seedSourceRun(t *testing.T, st *store.Store, team, run, remote, key, ref, sha string, appID int64, dispatch runspec.Dispatch) {
	t.Helper()
	now := time.Now().UTC()
	if err := teststore.SeedRun(st, t.Context(), store.Team(team), store.Run{ID: run, Pipeline: "build", Status: "pending", CreatedAt: now, RepoURL: remote, GitBranch: ref, GitSHA: sha}); err != nil {
		t.Fatal(err)
	}
	seal := runspec.SealedRequest{Version: 1, RunID: run, AdmittedAt: now, Request: runspec.Request{
		Version: 1, Team: team, Pipeline: "build", Mode: runspec.IntakeExecute, Dispatch: dispatch,
		Git:    runspec.Git{Repository: runspec.Repository{Key: runspec.RepoKey(key), Display: remote}, Ref: ref, SHA: sha},
		Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: remote}},
	}}
	if appID > 0 {
		seal.Request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{RepositoryID: appID, InstallationID: 7, Ref: ref, SHA: sha, Event: "push", DeliveryKey: "delivery-" + run, VerifiedAt: now, DefaultBranch: "main", DefaultBranchPush: ref == "refs/heads/main"}}
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
	body := controller.SealWith(t, seal, seal.Provenance)
	if _, err := st.DB().ExecContext(t.Context(), `UPDATE runs SET repo_key=?,repo_display=?,request_json=?,dispatch=? WHERE team=? AND id=?`, key, remote, body, dispatch, team, run); err != nil {
		t.Fatal(err)
	}
	kind := capability.UnitTrigger
	if dispatch == runspec.DispatchController {
		kind = capability.UnitPlan
	}
	if _, err := st.DB().ExecContext(t.Context(), `INSERT INTO triggers(team,id,unit_kind,status,created_at,available_at,expires_at,needs_labels,prefers_labels) VALUES (?,?,?,'pending',?,?,?,?,?)`, team, run, kind, now.UnixNano(), now.UnixNano(), now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
}

func githubClaimBody(kind capability.UnitKind, run, job string) controllerwire.ClaimRequest {
	return controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{kind}, RunID: run, JobID: job, Labels: []string{controller.GitHubActionsLabel}, AvailableCores: 8, AvailableMemoryBytes: 8 << 30}
}

func githubFixtureKey(id int64) string { return fmt.Sprintf("gh:%d", id) }
