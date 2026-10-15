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
	c, err := f.store.ClaimLaunch(store.WithoutCreditMetering(context.Background()),
		store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
		store.LaunchClaimRequest{HolderID: "launcher:" + runID, Lease: time.Minute, Deadline: time.Hour, RunID: runID, NodeID: nodeID},
		controller.LaunchClaimClock(f.t, f.store, runID, nodeID))
	if err != nil || c == nil {
		f.t.Fatalf("launch claim %s/%s: %+v %v", runID, nodeID, c, err)
	}
	return c.Token
}

// A pod's claim token reads its own run, beats its own claim, and starts its
// execution once; after that the claim gets no source credential, and a cancel
// reaches the pod through the beat while its reads are refused.
func TestClaimRun_PodRoutesServeOnlyTheirOwnClaim(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-pod", "acme", "widgets")
	f.launchedRun(olga, "run-next", "acme", "widgets")

	f.secretArg("run-pod")
	var run store.Run
	if code := f.call("GET", "/api/v1/runs/run-pod?include=secret_values", plan, nil, &run); code != http.StatusOK ||
		run.Pipeline != "build" || run.Args["token"] == "s3cret" {
		t.Fatalf("own run with a planning claim = %d %+v, want the secret argument redacted", code, run)
	}
	if code := f.call("GET", "/api/v1/triggers/run-pod", plan, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("the run's trigger, which holds its arguments unredacted = %d, want 401", code)
	}
	for _, path := range []string{"/api/v1/runs/run-next", "/api/v1/runs/run-next/nodes/plan/heartbeat", "/api/v1/runs/run-pod/nodes/other/heartbeat"} {
		method := "POST"
		if !strings.HasSuffix(path, "heartbeat") {
			method = "GET"
		}
		if code := f.call(method, path, plan, map[string]int{}, nil); code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", method, path, code)
		}
	}
	var beat struct{ Cancel bool }
	if code := f.call("POST", "/api/v1/runs/run-pod/nodes/plan/heartbeat", plan, map[string]int{"lease_secs": 120}, &beat); code != http.StatusOK || beat.Cancel {
		t.Fatalf("beat = %d %+v", code, beat)
	}
	if code := f.call("POST", "/api/v1/runs/run-pod/nodes/plan/execution-start", plan, nil, nil); code != http.StatusOK {
		t.Fatalf("execution start = %d", code)
	}
	var refused map[string]any
	if code := f.call("POST", "/api/v1/runs/run-pod/source-credential", plan, nil, &refused); code != http.StatusForbidden ||
		refused["error"] != "source_credential_spent" {
		t.Fatalf("source credential after the start = %d %v, want 403 source_credential_spent", code, refused)
	}
	for _, path := range []string{"/api/v1/launcher/sync", "/api/v1/runs/run-pod/children"} {
		if code := f.call("POST", path, plan, map[string]any{}, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("POST %s with a plan claim = %d, want refused", path, code)
		}
	}
	if err := f.teamOf(olga).RequestCancel(context.Background(), "run-pod"); err != nil {
		t.Fatal(err)
	}
	if code := f.call("POST", "/api/v1/runs/run-pod/nodes/plan/heartbeat", plan, map[string]int{}, &beat); code != http.StatusOK || !beat.Cancel {
		t.Fatalf("beat after cancel = %d %+v, want the cancel", code, beat)
	}
	if code := f.call("GET", "/api/v1/runs/run-pod", plan, nil, nil); code != http.StatusForbidden {
		t.Fatalf("read after cancel = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/runs/run-pod/nodes/plan/execution-start", plan, nil, nil); code != http.StatusForbidden {
		t.Fatalf("execution start after cancel = %d, want 403", code)
	}
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
		if err := tn.CreateOrReplaceSecret(sealedSecret(t, store.Team(olga.team), sec), time.Now()); err != nil {
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
func TestClaimRun_SecretsReachOnlyALiveWorkClaimAndOnlyDeclaredNames(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-sec", "acme", "widgets")
	f.launchedRun(olga, "run-other", "acme", "widgets")
	tn, err := f.store.ForTeam(context.Background(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DEPLOY_TOKEN", "UNDECLARED"} {
		if err := tn.CreateOrReplaceSecret(sealedSecret(t, store.Team(olga.team), store.Secret{Name: name, Value: "v-" + name, Pipeline: "build", Masked: true}), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	doc := map[string]any{
		"nodes":   []map[string]any{{"id": "a", "deps": []string{}, "spec_hash": specA}},
		"secrets": []map[string]any{{"name": "DEPLOY_TOKEN", "required": true}},
	}
	if code := f.call("GET", "/api/v1/secrets/DEPLOY_TOKEN?run=run-sec", plan, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a planning claim's secret read = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/runs/run-sec/plan", plan, doc, nil); code != http.StatusOK {
		t.Fatalf("accept plan = %d", code)
	}
	work := "Bearer " + f.launchNode("run-sec", "a")
	var sec struct{ Value string }
	if code := f.call("GET", "/api/v1/secrets/DEPLOY_TOKEN?run=run-sec", work, nil, &sec); code != http.StatusOK || sec.Value != "v-DEPLOY_TOKEN" {
		t.Fatalf("declared secret = %d %+v", code, sec)
	}
	var refused map[string]any
	if code := f.call("GET", "/api/v1/secrets/UNDECLARED?run=run-sec", work, nil, &refused); code != http.StatusForbidden || refused["error"] != "secret_undeclared" {
		t.Fatalf("undeclared secret = %d %v, want 403 secret_undeclared", code, refused)
	}
	if msg := fmt.Sprint(refused["message"]); !strings.Contains(msg, `pipeline "build" read secret "UNDECLARED", which it does not declare.`) ||
		!strings.Contains(msg, "add a field tagged `sw:\"UNDECLARED\"` to the struct its Secrets() method returns") {
		t.Fatalf("the undeclared refusal does not point at the declaration: %q", msg)
	}
	var probes []string
	for _, line := range strings.Split(f.logs.String(), "\n") {
		if strings.Contains(line, "event=secret_undeclared") {
			probes = append(probes, line)
		}
	}
	if len(probes) != 1 {
		t.Fatalf("audited %d undeclared-secret probes, want 1: %q", len(probes), probes)
	}
	_, id, _ := strings.Cut(probes[0], "principal_id=")
	id, _, _ = strings.Cut(id, " ")
	for _, want := range []string{"principal_kind=claim", "team=" + olga.team, "run_id=run-sec", "node_id=a"} {
		if !strings.Contains(probes[0], want) || id == "" || !strings.HasPrefix(strings.TrimPrefix(work, "Bearer "), id) {
			t.Fatalf("the probe's audit record %q lacks %s or the claim's token prefix", probes[0], want)
		}
	}
	if code := f.call("GET", "/api/v1/secrets/DEPLOY_TOKEN?run=run-other", work, nil, nil); code != http.StatusForbidden {
		t.Fatalf("another run's secret = %d, want 403", code)
	}
	if code := f.call("GET", "/api/v1/secrets/DEPLOY_TOKEN", work, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a secret read naming no run = %d, want 403", code)
	}
	secTeam, err := f.store.ForTeam(context.Background(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	events, err := secTeam.ListEventsAfter(context.Background(), "run-sec", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	released := 0
	for _, e := range events {
		if e.Kind == "secret_released" && e.NodeID == "a" && strings.Contains(string(e.Payload), "DEPLOY_TOKEN") {
			released++
		}
	}
	if released != 1 {
		t.Fatalf("recorded %d secret releases, want 1: %+v", released, events)
	}
	if err := f.teamOf(olga).RequestCancel(context.Background(), "run-sec"); err != nil {
		t.Fatal(err)
	}
	if code := f.call("GET", "/api/v1/secrets/DEPLOY_TOKEN?run=run-sec", work, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a secret read after cancel = %d, want 403", code)
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
