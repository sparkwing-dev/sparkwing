package controller

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestFreshChildReplayIgnoresBranchMovementAndSourceOutage(t *testing.T) {
	repo, original := sourcetest.Repository(t)
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repo})
	sourcetest.Git(t, repo, "checkout", "-qb", "deploy")
	sourcetest.Write(t, filepath.Join(repo, ".sparkwing", "pipeline.go"), "package main // selected child\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "child")
	selected := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	key := runspec.RepoKey("git:8.8.8.8/team/repo")
	e, h, claim := childIntakeFixtureWith(t, func(e *engineFixture) {
		tenant, err := e.Store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		parent, err := tenant.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		parent.Request.Request.Git.SHA = original
		parent.Request.Request.Git.Repository = runspec.Repository{Key: key, Display: remote}
		parent.Request.Request.Source = runspec.Source{Ref: original, Location: runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"}}
		sealed, err := json.Marshal(parent.Request)
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, e.Store, `UPDATE runs SET request_json=?,repo_key=? WHERE team='a' AND id='run'`, sealed, string(key))
	})
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	intake := func(args map[string]string) string {
		request := runspec.Request{
			Version: 1, Pipeline: "deploy", RequestedArgs: args, Origin: runspec.OriginChild, Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController,
			Git:     runspec.Git{Repository: parent.Request.Request.Git.Repository, Ref: "deploy"},
			Source:  runspec.Source{Location: parent.Request.Request.Source.Location, CommittedCheckout: true},
			Lineage: runspec.Lineage{ParentRunID: "run", ParentJobID: "job"},
		}
		body, err := json.Marshal(controllerwire.CreateRunRequest{Request: request, IdempotencyKey: "fresh-child"})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	admitted := func(w interface{ Bytes() []byte }) controllerwire.CreateRunResponse {
		var response controllerwire.CreateRunResponse
		if err := json.Unmarshal(w.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := admitted(engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", intake(nil), http.StatusCreated).Body)
	if first.Request.Request.Git.SHA != selected {
		t.Fatalf("child sealed %q, want %q", first.Request.Request.Git.SHA, selected)
	}
	sourcetest.Write(t, filepath.Join(repo, "advance.txt"), "advance\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "advance")
	replayed := admitted(engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", intake(nil), http.StatusOK).Body)
	if !replayed.Replayed || replayed.RunID != first.RunID || replayed.Request.Request.Git.SHA != selected {
		t.Fatalf("advanced branch replay = %+v, want run %s at %s", replayed, first.RunID, selected)
	}
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", intake(map[string]string{"changed": "yes"}), http.StatusConflict)
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", intake(map[string]string{"changed": "after outage"}), http.StatusConflict)
	replayed = admitted(engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", intake(nil), http.StatusOK).Body)
	if replayed.RunID != first.RunID || replayed.Request.Request.Git.SHA != selected {
		t.Fatalf("source outage replay = %+v, want run %s", replayed, first.RunID)
	}
}
