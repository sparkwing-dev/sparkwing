package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
		time.Now())
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
	if err := f.store.RequestCancel(context.Background(), "run-pod"); err != nil {
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
	child, err := f.store.GetTrigger(context.Background(), childID)
	if err != nil || child.GitSHA != headSHA || child.ParentRunID != "run-parent" || child.ParentNodeID != "a" {
		t.Fatalf("child trigger = %+v %v", child, err)
	}
	var repoID int64
	if err := f.store.DB().QueryRow(`SELECT github_repo_id FROM triggers WHERE id = ?`, childID).Scan(&repoID); err != nil || repoID != 701 {
		t.Fatalf("child repository id = %d %v, want the parent's 701", repoID, err)
	}
	if _, err := f.store.GetNode(context.Background(), childID, store.PlanNodeID); err != nil {
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
		if err := tn.CreateOrReplaceSecret(store.Secret{Name: name, Value: "v-" + name, Pipeline: "build", Masked: true}, time.Now()); err != nil {
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
	var oidc map[string]any
	if code := f.call("POST", "/api/v1/runs/run-sec/oidc-token", work, map[string]string{"audience": "sts.amazonaws.com"}, &oidc); code != http.StatusUnprocessableEntity ||
		!strings.Contains(fmt.Sprint(oidc["error"]), "not yet issued to controller-dispatched nodes") {
		t.Fatalf("an OIDC token for a work claim = %d %v, want 422 naming the gap", code, oidc)
	}
	var refused map[string]any
	if code := f.call("GET", "/api/v1/secrets/UNDECLARED?run=run-sec", work, nil, &refused); code != http.StatusForbidden || refused["error"] != "secret_undeclared" {
		t.Fatalf("undeclared secret = %d %v, want 403 secret_undeclared", code, refused)
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
	events, err := f.store.ListEventsAfter(context.Background(), "run-sec", 0, 100)
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
	if err := f.store.RequestCancel(context.Background(), "run-sec"); err != nil {
		t.Fatal(err)
	}
	if code := f.call("GET", "/api/v1/secrets/DEPLOY_TOKEN?run=run-sec", work, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a secret read after cancel = %d, want 403", code)
	}
}

// A work claim takes and returns concurrency slots for its own run in its
// own team, and only as its node's accepted plan declares them: another run,
// another node, another key or policy, another run's holder ID or holder, a
// planning claim and a cancelled run's new acquire are refused, neither an
// acquire nor a heartbeat holds the lease past the claim, and a release after cancel still frees the slot.
func TestClaimRun_ConcurrencySlotsFollowTheAcceptedPlan(t *testing.T) {
	ctx := context.Background()
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-slot", "acme", "widgets")
	f.launchedRun(olga, "run-stranger", "acme", "widgets")
	if code := f.call("POST", "/api/v1/concurrency/g:deploy/acquire", plan, map[string]any{
		"holder_id": "h", "run_id": "run-slot", "node_id": "plan", "max": 1, "cost": 1, "policy": "queue",
	}, nil); code != http.StatusForbidden {
		t.Fatalf("a planning claim's acquire = %d, want 403", code)
	}
	doc := map[string]any{"nodes": []map[string]any{{"id": "a", "deps": []string{}, "spec_hash": specA, "modifiers": map[string]any{
		"conc_group": "deploy", "conc_capacity": 1, "conc_cost": 1, "conc_on_limit": "queue",
	}}}}
	if code := f.call("POST", "/api/v1/runs/run-slot/plan", plan, doc, nil); code != http.StatusOK {
		t.Fatalf("accept plan = %d", code)
	}
	wk := "Bearer " + f.launchNode("run-slot", "a")
	body := func(run, node, policy string, lease int) map[string]any {
		return map[string]any{"holder_id": run + "/" + node, "run_id": run, "node_id": node, "max": 1, "cost": 1, "policy": policy, "lease_secs": lease}
	}
	foreignHolder := body("run-slot", "a", "queue", 0)
	foreignHolder["holder_id"] = "run-stranger/a"
	ownHolderOtherNode := body("run-slot", "b", "queue", 0)
	ownHolderOtherNode["holder_id"] = "run-slot/a"
	tn, err := f.store.ForTeam(ctx, store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	if res, err := tn.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "g:deploy", HolderID: "run-stranger/a", RunID: "run-stranger", NodeID: "a", Capacity: 1, Policy: "queue",
	}); err != nil || res.Kind != store.AcquireGranted {
		t.Fatalf("seed another run's holder = %+v %v", res, err)
	}
	joinStranger := body("run-slot", "a", "queue", 0)
	joinStranger["inherited_holder_id"] = "run-stranger/a"
	for _, c := range []struct {
		key  string
		body map[string]any
		what string
	}{
		{"g:deploy", body("run-stranger", "a", "queue", 0), "another run's acquire"},
		{"g:deploy", body("run-slot", "b", "queue", 0), "another node's acquire"},
		{"g:other", body("run-slot", "a", "queue", 0), "an undeclared key"},
		{"g:deploy", body("run-slot", "a", "cancel_others", 0), "an undeclared policy"},
		{"g:deploy", foreignHolder, "another run's holder ID"},
		{"g:deploy", ownHolderOtherNode, "another node's acquire under its own holder ID"},
		{"g:deploy", joinStranger, "joining another run's holder"},
	} {
		if code := f.call("POST", "/api/v1/concurrency/"+c.key+"/acquire", wk, c.body, nil); code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", c.what, code)
		}
	}
	if _, _, _, err := tn.ReleaseAndNotify(ctx, "g:deploy", "run-stranger/a", "success", "", "", 0, store.DefaultConcurrencyLease); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if code := f.call("POST", "/api/v1/concurrency/g:deploy/acquire", wk, body("run-slot", "a", "queue", 30*24*3600), &got); code != http.StatusOK || got["granted"] != true {
		t.Fatalf("the declared acquire = %d %v", code, got)
	}
	var beat struct {
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
	}
	if code := f.call("POST", "/api/v1/concurrency/g:deploy/heartbeat", wk, map[string]any{"holder_id": "run-slot/a", "lease_secs": 30 * 24 * 3600}, &beat); code != http.StatusOK || beat.LeaseExpiresAt.After(time.Now().Add(2*time.Hour)) {
		t.Fatalf("a heartbeat asking for 30 days = %d, lease to %v, want 200 and no longer than the claim token", code, beat.LeaseExpiresAt)
	}
	for team, want := range map[store.Team]int{store.Team(olga.team): 1, store.DefaultTeam: 0} {
		tn, err := f.store.ForTeam(ctx, team)
		if err != nil {
			t.Fatal(err)
		}
		st, err := tn.GetConcurrencyState(ctx, "g:deploy")
		held := 0
		if err == nil {
			held = len(st.Holders)
			for _, h := range st.Holders {
				if h.LeaseExpiresAt.After(time.Now().Add(2 * time.Hour)) {
					t.Errorf("the holder's lease runs to %v, past the claim token", h.LeaseExpiresAt)
				}
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		if held != want {
			t.Fatalf("team %s holds %d slots of deploy, want %d", team, held, want)
		}
	}
	if err := f.store.RequestCancel(ctx, "run-slot"); err != nil {
		t.Fatal(err)
	}
	if code := f.call("POST", "/api/v1/concurrency/g:deploy/acquire", wk, body("run-slot", "a", "queue", 0), nil); code != http.StatusForbidden {
		t.Fatalf("an acquire after cancel = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/concurrency/g:deploy/release", wk, map[string]any{"holder_id": "run-slot/a", "outcome": "cancelled"}, nil); code != http.StatusNoContent {
		t.Fatalf("a release after cancel = %d, want 204", code)
	}
}

// A work claim's durable-log validation answers with the claim's team only
// for the claim's own attempt; any attempt, claim or trigger header is
// refused, even an attempt ordinal on its own.
func TestClaimRun_LogValidationBindsTheClaimsOwnAttempt(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-log", "acme", "widgets")
	doc := map[string]any{"nodes": []map[string]any{{"id": "a", "deps": []string{}, "spec_hash": specA}}}
	if code := f.call("POST", "/api/v1/runs/run-log/plan", plan, doc, nil); code != http.StatusOK {
		t.Fatalf("accept plan = %d", code)
	}
	work := "Bearer " + f.launchNode("run-log", "a")
	var lastHeader http.Header
	validate := func(headers map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, f.url+"/api/v1/runs/run-log/nodes/a/claim/validate", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", work)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		lastHeader = resp.Header
		return resp.StatusCode, resp.Header.Get(store.ClaimTeamHeader)
	}
	if code, team := validate(nil); code != http.StatusNoContent || team != olga.team {
		t.Fatalf("own attempt's validation = %d team %q, want 204 team %q", code, team, olga.team)
	}
	if gen, ord := lastHeader.Get(store.ClaimGenerationHeader), lastHeader.Get(store.AttemptOrdinalHeader); gen == "" || gen == "0" || ord != "1" {
		t.Fatalf("own attempt's validation names generation %q ordinal %q, want the claim's generation and ordinal 1", gen, ord)
	}
	for what, headers := range map[string]map[string]string{
		"an attempt ordinal": {store.AttemptOrdinalHeader: "2"},
		"a trigger stream":   {store.TriggerGenerationHeader: "4"},
		"another claim": {
			store.ClaimHolderHeader: "h", store.ClaimMembershipHeader: "m", store.ClaimReservationHeader: "r",
			store.ClaimGenerationHeader: "9", store.AttemptOrdinalHeader: "1",
		},
	} {
		if code, team := validate(headers); code != http.StatusForbidden || team != "" {
			t.Errorf("validation naming %s = %d team %q, want 403 and no team", what, code, team)
		}
	}
}

// A work claim reads another run's output only through a reference its plan
// declares, and the controller picks the run: a memoized result through its
// cache entry, the leader its own coalesce waiter names, and the newest
// successful run of a declared pipeline reference. A memoized result reaches
// it only under its own node's key and only from its own repository, pipeline
// and node; an undeclared reference, another node's waiter or key, and a read
// naming another run are refused or find nothing, and a refusal is audited.
func TestClaimRun_InputsFromAnotherRunFollowTheAcceptedPlan(t *testing.T) {
	ctx := context.Background()
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	tn, err := f.store.ForTeam(ctx, store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	// safety: outputs count toward the team's storage, which a team with no
	// credits and no free slot cannot hold.
	if _, err := tn.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCent, "pay_outputs", "root"); err != nil {
		t.Fatal(err)
	}
	accept := func(runID string, nodes ...map[string]any) {
		t.Helper()
		for _, n := range nodes {
			n["deps"], n["spec_hash"] = []string{}, specA
		}
		plan := "Bearer " + f.launchedRun(olga, runID, "acme", "widgets")
		if code := f.call("POST", "/api/v1/runs/"+runID+"/plan", plan, map[string]any{"nodes": nodes}, nil); code != http.StatusOK {
			t.Fatalf("accept %s's plan = %d", runID, code)
		}
	}
	memo := map[string]any{"cache": true}
	finished := func(runID, nodeID string, modifiers map[string]any, output string) {
		t.Helper()
		accept(runID, map[string]any{"id": nodeID, "modifiers": modifiers})
		raw := f.launchNode(runID, nodeID)
		ref, err := client.NewWithToken(f.url, nil, raw).UploadNodeOutput(ctx, runID, nodeID, []byte(output))
		if err != nil {
			t.Fatalf("upload %s/%s's output: %v", runID, nodeID, err)
		}
		if code := f.call("POST", "/api/v1/runs/"+runID+"/nodes/"+nodeID+"/attempt", "Bearer "+raw,
			map[string]any{"outcome": "success", "output": ref}, nil); code != http.StatusOK {
			t.Fatalf("finish %s/%s = %d", runID, nodeID, code)
		}
	}
	finished("run-leader", "c", memo, `{"from":"leader"}`)
	finished("run-stranger", "m", memo, `{"from":"stranger"}`)
	finished("run-origin", "m", memo, `{"from":"cache"}`)
	finished("run-node", "x", memo, `{"from":"another node"}`)
	finished("run-pipeline", "m", memo, `{"from":"another pipeline"}`)
	finished("run-repo", "m", memo, `{"from":"another repository"}`)
	finished("run-last", "build", nil, `{"from":"last"}`)
	for _, q := range []string{
		`UPDATE runs SET pipeline = 'deploy' WHERE id = 'run-pipeline'`,
		`UPDATE triggers SET github_repo_id = 702 WHERE id = 'run-repo'`,
	} {
		if _, err := f.store.DB().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	accept("run-in",
		map[string]any{"id": "m", "modifiers": memo},
		map[string]any{"id": "c", "modifiers": memo},
		map[string]any{"id": "r", "pipeline_refs": []map[string]string{{"pipeline": "build", "node": "build"}}})
	ns := func(node, hash string) string { return store.ClaimMemoKey("acme/widgets", "build", node, hash) }
	now := time.Now().UnixNano()
	cache := func(team, key, hash, run, node string) {
		t.Helper()
		if _, err := f.store.DB().ExecContext(ctx, `INSERT INTO concurrency_cache
 (team, key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
 VALUES (?, ?, ?, '', ?, ?, ?, ?, ?)`, team, key, hash, run, node, now, now+int64(time.Hour), now); err != nil {
			t.Fatal(err)
		}
	}
	waiter := func(key, hash, node, leaderRun, leaderNode string) {
		t.Helper()
		if _, err := f.store.DB().ExecContext(ctx, `INSERT INTO concurrency_waiters
 (team, key, run_id, node_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id)
 VALUES (?, ?, 'run-in', ?, ?, 'coalesce', ?, ?, ?)`, olga.team, key, node, now, hash, leaderRun, leaderNode); err != nil {
			t.Fatal(err)
		}
	}
	cache(olga.team, ns("m", "h1"), "h1", "run-origin", "m")
	cache(olga.team, ns("c", "h1"), "h1", "run-leader", "c")
	waiter(ns("c", "h2"), "h2", "c", "run-leader", "c")
	waiter(ns("m", "h3"), "h3", "m", "run-stranger", "m")
	cache(string(store.DefaultTeam), ns("m", "h4"), "h4", "run-stranger", "m")
	cache(olga.team, ns("m", "h5"), "h5", "run-node", "x")
	cache(olga.team, ns("m", "h6"), "h6", "run-pipeline", "m")
	cache(olga.team, ns("m", "h7"), "h7", "run-repo", "m")
	tokens, raws := map[string]string{}, map[string]string{}
	for _, n := range []string{"m", "c", "r"} {
		raws[n] = f.launchNode("run-in", n)
		tokens[n] = "Bearer " + raws[n]
	}
	input := func(node string, req store.ClaimInputRequest) (int, store.ClaimInput) {
		t.Helper()
		var in store.ClaimInput
		code := f.call("POST", "/api/v1/runs/run-in/nodes/"+node+"/claim/input", tokens[node], req, &in)
		return code, in
	}
	memoIn := func(kind store.ClaimInputKind, node, hash string) store.ClaimInputRequest {
		return store.ClaimInputRequest{Kind: kind, Key: ns(node, hash), CacheKeyHash: hash}
	}
	lastRun := store.ClaimInputRequest{Kind: store.ClaimInputLastRun, Pipeline: "build", Node: "build"}
	for _, c := range []struct {
		node        string
		req         store.ClaimInputRequest
		run, output string
	}{
		{"m", memoIn(store.ClaimInputCached, "m", "h1"), "run-origin", `{"from":"cache"}`},
		{"c", memoIn(store.ClaimInputCoalesced, "c", "h2"), "run-leader", `{"from":"leader"}`},
		{"c", memoIn(store.ClaimInputCoalesced, "c", "h1"), "run-leader", `{"from":"leader"}`},
		{"r", lastRun, "run-last", `{"from":"last"}`},
	} {
		in, data, err := client.NewWithToken(f.url, nil, raws[c.node]).ClaimInput(ctx, "run-in", c.node, c.req)
		if err != nil || in.RunID != c.run || string(data) != c.output {
			t.Errorf("node %s's %s input %s = %v %s %s, want %s %s", c.node, c.req.Kind, c.req.Key, err, in.RunID, data, c.run, c.output)
		}
	}
	bare, otherRef := memoIn(store.ClaimInputCached, "m", "h1"), lastRun
	bare.Key = "memo:h1"
	otherRef.Node = "lead"
	for what, c := range map[string]struct {
		node string
		req  store.ClaimInputRequest
		want int
	}{
		"a cache entry on a node that declares no memoization": {"r", memoIn(store.ClaimInputCached, "r", "h1"), http.StatusForbidden},
		"a sibling node's key for a known hash":                {"m", memoIn(store.ClaimInputCached, "c", "h1"), http.StatusForbidden},
		"a bare memo key for a known hash":                     {"m", bare, http.StatusForbidden},
		"an entry another node wrote under its key":            {"m", memoIn(store.ClaimInputCached, "m", "h5"), http.StatusForbidden},
		"an entry another pipeline wrote under its key":        {"m", memoIn(store.ClaimInputCached, "m", "h6"), http.StatusForbidden},
		"an entry another repository wrote under its key":      {"m", memoIn(store.ClaimInputCoalesced, "m", "h7"), http.StatusForbidden},
		"an undeclared pipeline reference":                     {"r", otherRef, http.StatusForbidden},
		"a pipeline reference on a node that declares none":    {"m", lastRun, http.StatusForbidden},
		"another node's coalesce waiter":                       {"c", memoIn(store.ClaimInputCoalesced, "c", "h3"), http.StatusNotFound},
		"a key with no waiter and no cache entry":              {"m", memoIn(store.ClaimInputCoalesced, "m", "h9"), http.StatusNotFound},
		"another team's cache entry":                           {"m", memoIn(store.ClaimInputCached, "m", "h4"), http.StatusNotFound},
	} {
		if code, in := input(c.node, c.req); code != c.want {
			t.Errorf("%s = %d from %s, want %d", what, code, in.RunID, c.want)
		}
	}
	if !strings.Contains(f.logs.String(), "event=input_undeclared") {
		t.Error("a refused input was not audited")
	}
	if code := f.call("POST", "/api/v1/runs/run-leader/nodes/c/claim/input", tokens["c"], memoIn(store.ClaimInputCoalesced, "c", "h2"), nil); code != http.StatusForbidden {
		t.Errorf("an input read naming the leader's run in its path = %d, want 403", code)
	}
	if code := f.call("GET", "/api/v1/runs/run-stranger/nodes/m/output", tokens["m"], nil, nil); code == http.StatusOK {
		t.Errorf("another run's output read directly = %d, want refused", code)
	}
}

// A claim takes a memo slot only under its own repository, pipeline and node,
// and heartbeats, observes and releases only its own node's holder, never a
// sibling's in the same run.
func TestClaimRun_MemoSlotsAndHoldersStayInTheClaimsNode(t *testing.T) {
	ctx := context.Background()
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-memo", "acme", "widgets")
	memo := map[string]any{"cache": true}
	doc := map[string]any{"nodes": []map[string]any{
		{"id": "a", "deps": []string{}, "spec_hash": specA, "modifiers": memo},
		{"id": "b", "deps": []string{}, "spec_hash": specA, "modifiers": memo},
	}}
	if code := f.call("POST", "/api/v1/runs/run-memo/plan", plan, doc, nil); code != http.StatusOK {
		t.Fatalf("accept plan = %d", code)
	}
	wk := "Bearer " + f.launchNode("run-memo", "a")
	acquire := func(key string) int {
		return f.call("POST", "/api/v1/concurrency/"+url.PathEscape(key)+"/acquire", wk, map[string]any{
			"holder_id": "run-memo/a", "run_id": "run-memo", "node_id": "a", "max": 1, "cost": 1, "policy": "coalesce",
		}, nil)
	}
	for what, key := range map[string]string{
		"a bare memo key":          "memo:h1",
		"a sibling node's key":     store.ClaimMemoKey("acme/widgets", "build", "b", "h1"),
		"another pipeline's key":   store.ClaimMemoKey("acme/widgets", "deploy", "a", "h1"),
		"another repository's key": store.ClaimMemoKey("acme/plans", "build", "a", "h1"),
	} {
		if code := acquire(key); code != http.StatusForbidden {
			t.Errorf("acquiring %s = %d, want 403", what, code)
		}
	}
	own := store.ClaimMemoKey("acme/widgets", "build", "a", "h1")
	if code := acquire(own); code != http.StatusOK {
		t.Fatalf("acquiring its own memo key = %d, want 200", code)
	}
	tn, err := f.store.ForTeam(ctx, store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	sibling := store.ClaimMemoKey("acme/widgets", "build", "b", "h2")
	if res, err := tn.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: sibling, HolderID: "run-memo/b", RunID: "run-memo", NodeID: "b", Capacity: 1, Policy: "coalesce",
	}); err != nil || res.Kind != store.AcquireGranted {
		t.Fatalf("seed the sibling's holder = %+v %v", res, err)
	}
	for _, c := range []struct{ method, path, key string }{
		{"POST", "/heartbeat", sibling},
		{"POST", "/release", sibling},
		{"GET", "/holder?holder_id=run-memo%2Fb", sibling},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]any{"holder_id": "run-memo/b", "outcome": "success"}
		}
		if code := f.call(c.method, "/api/v1/concurrency/"+url.PathEscape(c.key)+c.path, wk, body, nil); code != http.StatusForbidden {
			t.Errorf("%s %s on the sibling's holder = %d, want 403", c.method, c.path, code)
		}
	}
	if code := f.call("POST", "/api/v1/concurrency/"+url.PathEscape(own)+"/heartbeat", wk, map[string]any{"holder_id": "run-memo/a"}, nil); code != http.StatusOK {
		t.Errorf("heartbeating its own holder = %d, want 200", code)
	}
	if st, err := tn.GetConcurrencyState(ctx, sibling); err != nil || len(st.Holders) != 1 {
		t.Fatalf("the sibling's holder = %+v %v, want it untouched", st, err)
	}
}
