package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const specA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (f *appFixture) secretArg(runID string) {
	f.t.Helper()
	if _, err := f.store.DB().Exec(`UPDATE runs SET args_json = ?, invocation_json = ? WHERE id = ?`,
		`{"token":"s3cret"}`, `{"secret_args":["token"]}`, runID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *appFixture) launchNode(runID, nodeID string) string {
	f.t.Helper()
	return controller.LauncherFixtureClaim(f.t, f.store, runID, nodeID)
}

// A node's child runs its parent's commit on the controller path, one child
// per call however often the call is retried, and only its parent reads it.
func TestClaimRun_ChildRunsInheritTheParentAndAnswerOnlyIt(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-parent", "acme", "widgets")
	doc := map[string]any{"nodes": []map[string]any{
		{"id": "a", "deps": []string{}, "spec_hash": specA}, {"id": "b", "deps": []string{}, "spec_hash": specA},
	}}
	if code := f.call("POST", "/api/v1/runs/run-parent/plan", plan, doc, nil); code != http.StatusOK {
		t.Fatalf("accept plan = %d", code)
	}
	work := "Bearer " + f.launchNode("run-parent", "a")
	sibling := "Bearer " + f.launchNode("run-parent", "b")
	f.secretArg("run-parent")
	var own store.Run
	if code := f.call("GET", "/api/v1/runs/run-parent?include=secret_values", work, nil, &own); code != http.StatusOK || own.Args["token"] != "s3cret" {
		t.Fatalf("own run with a work claim = %d %+v, want the secret argument", code, own.Args)
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	if code := f.call("POST", "/api/v1/runs/run-parent/children", work, map[string]any{"ordinal": 0, "pipeline": "deploy"}, &out); code != http.StatusAccepted || out.RunID == "" {
		t.Fatalf("enqueue = %d %+v", code, out)
	}
	childID := out.RunID
	if code := f.call("POST", "/api/v1/runs/run-parent/children", work, map[string]any{"ordinal": 0, "pipeline": "deploy"}, &out); code != http.StatusAccepted || out.RunID != childID {
		t.Fatalf("retried enqueue = %d %s, want %s", code, out.RunID, childID)
	}
	child, err := f.teamOf(olga).GetTrigger(context.Background(), childID)
	if err != nil || child.GitSHA != headSHA || child.ParentRunID != "run-parent" || child.ParentNodeID != "a" {
		t.Fatalf("child trigger = %+v %v", child, err)
	}
	var repoID int64
	if err := f.store.DB().QueryRow(`SELECT github_repo_id FROM triggers WHERE id = ?`, childID).Scan(&repoID); err != nil || repoID != 701 {
		t.Fatalf("child repository id = %d %v, want the parent's 701", repoID, err)
	}
	team, err := f.store.ForTeam(context.Background(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.GetNode(context.Background(), childID, store.PlanNodeID); err != nil {
		t.Fatalf("the child of an opted-in repository has no planning node: %v", err)
	}
	for body, want := range map[string]int{
		`{"ordinal":1,"pipeline":"deploy","repo":"bob/tools"}`: http.StatusUnprocessableEntity,
		`{"ordinal":1,"pipeline":"build"}`:                     http.StatusConflict,
	} {
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		if code := f.call("POST", "/api/v1/runs/run-parent/children", work, m, nil); code != want {
			t.Errorf("enqueue %s = %d, want %d", body, code, want)
		}
	}
	var got store.Run
	if code := f.call("GET", "/api/v1/runs/run-parent/children/"+childID, work, nil, &got); code != http.StatusOK || got.ID != childID {
		t.Fatalf("read child = %d %+v", code, got)
	}
	f.launchedRun(olga, "run-stranger", "acme", "widgets")
	for _, path := range []string{"/api/v1/runs/run-parent/children/" + childID, "/api/v1/runs/run-parent/children/" + childID + "/nodes/plan/output"} {
		if code := f.call("GET", path, sibling, nil, nil); code != http.StatusNotFound {
			t.Fatalf("a sibling node reading %s = %d, want 404", path, code)
		}
	}
	if code := f.call("GET", "/api/v1/runs/run-parent/children/run-stranger", work, nil, nil); code != http.StatusNotFound {
		t.Fatalf("read a run that is not a child = %d, want 404", code)
	}
	if code := f.call("GET", "/api/v1/runs/run-parent/children/"+childID, plan, nil, nil); code != http.StatusForbidden {
		t.Fatalf("read the child with the ended plan claim = %d, want 403", code)
	}
}

func (f *appFixture) teamOf(u signedIn) *store.Tenant {
	f.t.Helper()
	tn, err := f.store.ForTeam(context.Background(), store.Team(u.team))
	if err != nil {
		f.t.Fatal(err)
	}
	return tn
}

func (f *appFixture) trigger(id string) (*store.Trigger, error) {
	f.t.Helper()
	team, err := f.store.AsOperator().RunTeam(context.Background(), id)
	if err != nil {
		return nil, err
	}
	tn, err := f.store.ForTeam(context.Background(), team)
	if err != nil {
		return nil, err
	}
	return tn.GetTrigger(context.Background(), id)
}
