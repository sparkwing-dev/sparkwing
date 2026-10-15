package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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

// A plan whose required secrets the run cannot read is refused with 422. The
// planning claim learns only which declared names the run cannot read, never
// whether a row exists elsewhere or which pipeline owns it; that detail, with
// the fix for each, goes to the run's events, which only the team reads.
func TestClaimRun_PlanRefusedWhenARequiredSecretIsMissing(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-missing", "acme", "widgets")
	tn, err := f.store.ForTeam(context.Background(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range []store.Secret{{Name: "TEAM_ONLY", Value: "v"}, {Name: "ELSEWHERE", Value: "v", Pipeline: "release"}} {
		sec.Masked = true
		if err := teststore.SeedSecret(f.store, tn.Team(), sealedSecret(t, store.Team(olga.team), sec), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	doc := map[string]any{
		"nodes": []map[string]any{{"id": "a", "deps": []string{}, "spec_hash": specA}},
		"secrets": []map[string]any{
			{"name": "DEPLOY_TOKEN", "required": true},
			{"name": "TEAM_ONLY"},
			{"name": "ELSEWHERE"},
			{"name": "SLACK_HOOK", "optional": true},
		},
	}
	var refused map[string]any
	if code := f.call("POST", "/api/v1/runs/run-missing/plan", plan, doc, &refused); code != http.StatusUnprocessableEntity {
		t.Fatalf("accept plan = %d %v, want 422", code, refused)
	}
	want := `pipeline "build" did not start: this run cannot read 3 required secrets it declares: DEPLOY_TOKEN, TEAM_ONLY, ELSEWHERE. ` +
		`A team member sees which scope each is missing from, and the command that sets it, with: ` +
		`sparkwing runs status --run run-missing --profile <profile>`
	if got := fmt.Sprint(refused["error"]); got != want {
		t.Fatalf("the planner's refusal =\n%s\nwant\n%s", got, want)
	}
	for _, leak := range []string{"team-wide", "shared", "release", "only for", "not set", "admin"} {
		if strings.Contains(fmt.Sprint(refused), leak) {
			t.Fatalf("the planner's refusal tells it %q: %v", leak, refused)
		}
	}
	if code := f.call("GET", "/api/v1/runs/run-missing/events", plan, nil, nil); code == http.StatusOK {
		t.Fatal("the planning claim read the run's events")
	}
	var events []store.Event
	if code := f.call("GET", "/api/v1/runs/run-missing/events", olga.auth, nil, &events); code != http.StatusOK {
		t.Fatalf("a team member's events read = %d", code)
	}
	var detail string
	for _, ev := range events {
		if ev.Kind == store.EventKindSecretPreflightRefused {
			var payload struct{ Message string }
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			detail = payload.Message
		}
	}
	for _, line := range []string{
		"- TEAM_ONLY is set team-wide but not shared with runs",
		`- ELSEWHERE is set only for pipeline "release"`,
		"sparkwing secrets set --name DEPLOY_TOKEN --file <path> --profile <profile> --pipeline build",
	} {
		if !strings.Contains(detail, line) {
			t.Fatalf("the team's detail lacks %q:\n%s", line, detail)
		}
	}
	if strings.Contains(detail, "SLACK_HOOK") {
		t.Fatalf("the detail names the optional SLACK_HOOK:\n%s", detail)
	}
	if _, err := tn.GetNode(context.Background(), "run-missing", store.PlanNodeID); err != nil {
		t.Fatalf("the run's own planning node: %v", err)
	}
	if _, err := tn.GetNode(context.Background(), "run-missing", "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("node of the refused plan: err = %v, want not found", err)
	}
}

// A work claim reads a secret its run's plan declares, and each read is
// recorded on its node; an undeclared name, a planning claim, another run
// and a cancelled run are all refused.
// A work claim takes and returns concurrency slots for its own run in its
// own team, and only as its node's accepted plan declares them: another run,
// another node, another key or policy, another run's holder ID or holder, a
// planning claim and a cancelled run's new acquire are refused, neither an
// acquire nor a heartbeat holds the lease past the claim, and a release after cancel still frees the slot.

// A claim takes a memo slot only under its own repository, pipeline and node,
// and heartbeats, observes and releases only its own node's holder, never a
// sibling's in the same run.

// Every concurrency route a claim reaches holds it to one rule: it acts only
// on its own node's holders and waiters, under a key its node declares. A
// sibling node's live holder in the same run survives every route aimed at it.

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
