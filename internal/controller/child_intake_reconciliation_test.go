package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func childIntakeFixture(t *testing.T) (*engineFixture, http.Handler, string) {
	return childIntakeFixtureWith(t, nil)
}

func childIntakeFixtureWith(t *testing.T, prepare func(*engineFixture)) (*engineFixture, http.Handler, string) {
	t.Helper()
	e, h, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim, capability.TriggersClaim})
	engineSQL(t, e.Store, `UPDATE runs SET lineage_json=? WHERE team='a' AND id='run'`, []byte(`{"admission_key":"capability:root","admission_subject":{"kind":"account","id":"fixture"}}`))
	declareChildren(t, e, "deploy", "Deploy", "ci", "root", "fresh")
	var req runspec.SealedRequest
	row, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := row.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	req = run.Request
	req.Request.RequestedArgs = map[string]string{"token": "s3cret"}
	req.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"token"}}
	req.Request.Git.Ref = "main"
	req.Request.Git.SHA = "0123456789abcdef0123456789abcdef01234567"
	req.Request.Source.Ref = req.Request.Git.SHA
	raw := sealWith(t, req, assertedProvenance(t, "ci", req.Request.Git.Ref, req.Request.Git.SHA))
	engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, raw)
	engineAcceptedFixturePlan(t, e, "a", "run")
	if prepare != nil {
		prepare(e)
	}
	claimed := engineClaimHTTP(t, h, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(claimed.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %s: %v", claimed.Body, err)
	}
	engineAdmitBearer(t, e.Store, award.Claim.Capability)
	engineClaimHTTP(t, h, award.Claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	return e, h, award.Claim.Capability
}

// declareChildren makes the parent job's plan declare the child pipelines it may start.
func declareChildren(t *testing.T, e *engineFixture, pipelines ...string) {
	t.Helper()
	children := make([]map[string]string, len(pipelines))
	for i, pipeline := range pipelines {
		children[i] = map[string]string{"pipeline": pipeline}
	}
	spec, err := json.Marshal(map[string]any{"id": "job", "children": children})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, spec)
}

func childIntakeBody(t *testing.T, pipeline string, git runspec.Git) string {
	t.Helper()
	if git.SHA == "" && git.Ref == "" {
		git.Ref, git.SHA = "main", "0123456789abcdef0123456789abcdef01234567"
	}
	body, err := json.Marshal(controllerwire.CreateRunRequest{IdempotencyKey: "child-" + pipeline, Request: runspec.Request{Version: 1, Team: "a", Pipeline: pipeline, Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController, Origin: runspec.OriginChild, Git: git, Source: runspec.Source{Ref: git.SHA}, Lineage: runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestClaimRun_ChildRunsInheritTheParentAndAnswerOnlyIt(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	private := engineClaimHTTP(t, h, claim, "GET", "/api/v1/runs/run/local-host/nodes/job/snapshot", "", 200)
	var snapshot store.RunSnapshot
	if err := json.Unmarshal(private.Body.Bytes(), &snapshot); err != nil || snapshot.Run.Request.Request.RequestedArgs["token"] != "s3cret" {
		t.Fatalf("private snapshot: %s: %v", private.Body, err)
	}
	reader, _, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "reader"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		t.Fatal(err)
	}
	public := engineClaimHTTP(t, h, reader, "GET", "/api/v1/runs/run?include=secret_values", "", 200)
	if strings.Contains(public.Body.String(), "s3cret") {
		t.Fatalf("public run disclosed secret: %s", public.Body)
	}
	body := childIntakeBody(t, "deploy", snapshot.Run.Request.Request.Git)
	first := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", body, 201)
	var created controllerwire.CreateRunResponse
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	replay := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", body, 200)
	var repeated controllerwire.CreateRunResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &repeated); err != nil || repeated.RunID != created.RunID || !repeated.Replayed || len(repeated.HereBearer) != 0 {
		t.Fatalf("replay: %s: %v", replay.Body, err)
	}
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	child, err := tenant.ReadRun(t.Context(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	lineage := child.Request.Request.Lineage
	if lineage.ParentRunID != "run" || lineage.ParentJobID != "job" || child.Request.Request.Git != snapshot.Run.Request.Request.Git || child.Dispatch != "controller" {
		t.Fatalf("child: %+v", child)
	}
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,queue_started_at,spec_json,execution_json) VALUES ('a','run','sibling','pending',1,?,?,?)`, time.Now().UnixNano(), []byte(`{"id":"sibling"}`), []byte(`{}`))
	root := engineHTTPRoot(t, e, "a", "sibling-runner", []capability.Grant{capability.NodesClaim})
	response := engineClaimHTTP(t, h, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 200)
	var sibling controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &sibling); err != nil || sibling.Claim == nil || sibling.Claim.Binding.NodeID != "sibling" {
		t.Fatalf("sibling: %s: %v", response.Body, err)
	}
	var kind string
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT unit_kind FROM triggers WHERE team='a' AND id=?`, created.RunID).Scan(&kind); err != nil || kind != "plan" {
		t.Fatalf("child owner unit=%q: %v", kind, err)
	}
	path := "/api/v1/runs/run/children/" + created.RunID
	engineClaimHTTP(t, h, claim, "GET", path, "", 200)
	for _, auth := range []string{sibling.Claim.Capability, root} {
		engineClaimHTTP(t, h, auth, "GET", path, "", 403)
		engineClaimHTTP(t, h, auth, "GET", path+"/jobs/plan/output", "", 403)
	}
	engineClaimHTTP(t, h, claim, "GET", "/api/v1/runs/run/children/run", "", 403)
	property := func(job string, variant uint8) bool {
		var changed controllerwire.CreateRunRequest
		if err := json.Unmarshal([]byte(body), &changed); err != nil {
			t.Fatal(err)
		}
		changed.IdempotencyKey = "forged-" + job
		want := 403
		switch variant % 3 {
		case 0:
			changed.Request.Lineage.ParentJobID = "forged-" + job
		case 1:
			changed.Request.Team = "foreign-" + job
		case 2:
			changed.Request.Lineage.ParentRunID = "forged-" + job
			want = 400
		}
		raw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		refused := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", string(raw), want)
		return !strings.Contains(refused.Body.String(), "s3cret")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("runs=%d: %v", count, err)
	}
	engineSQL(t, e.Store, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
	engineClaimHTTP(t, h, claim, "GET", path, "", 401)
}

func TestTeamBoundary_TriggerAncestryStaysInTheCallersTeam(t *testing.T) {
	property := func(seed uint32) bool {
		private := fmt.Sprintf("private-pipeline-%x", seed)
		e, h, claim := childIntakeFixtureWith(t, func(e *engineFixture) { declareChildren(t, e, "ci", private) })
		team, err := e.Store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		parent, err := team.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		teststore.SeedTeam(t, e.Store, "foreign")
		ancestor := fmt.Sprintf("ancestor-%x", seed)
		if err := teststore.SeedRun(e.Store, t.Context(), "foreign", store.Run{ID: ancestor, Pipeline: private, Status: "running"}); err != nil {
			t.Fatal(err)
		}
		cycle := childIntakeBody(t, parent.Request.Request.Pipeline, parent.Request.Request.Git)
		engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", cycle, 409)
		body := childIntakeBody(t, private, parent.Request.Request.Git)
		var replies []string
		for _, id := range []string{ancestor, ancestor + "-missing"} {
			raw, err := json.Marshal(runspec.Lineage{ParentRunID: id})
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `UPDATE runs SET lineage_json=? WHERE team='a' AND id='run'`, raw)
			response := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", body, 404)
			replies = append(replies, response.Body.String())
		}
		var count int
		if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return replies[0] == replies[1] && !strings.Contains(replies[0], private) && count == 1
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}

func TestChildIntakeCannotBroadenIssuedChildren(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	// A later declaration cannot enlarge an already issued job capability.
	declareChildren(t, e, "undeclared")
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "undeclared", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 403)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", runspec.Git{Repository: runspec.Repository{Key: "gh:2"}}), 403)
}

func TestChildIntakeRetainsRepositoryInheritanceWithoutGrantingSourceAuthority(t *testing.T) {
	property := func(inherited bool, seed uint32) bool {
		e, h, claim := childIntakeFixture(t)
		tenant, err := e.Store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		parent, err := tenant.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(childIntakeBody(t, "deploy", parent.Request.Request.Git)), &body); err != nil {
			t.Fatal(err)
		}
		request := body["request"].(map[string]any)
		request["lineage"].(map[string]any)["repo_inherited"] = inherited
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		w := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", string(raw), 201)
		var created controllerwire.CreateRunResponse
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		trigger, err := tenant.GetTrigger(t.Context(), created.RunID)
		if err != nil || trigger.RepoInherited != inherited {
			t.Fatalf("inheritance projection: %+v %v, want %t", trigger, err, inherited)
		}
		request["lineage"].(map[string]any)["repo_inherited"] = !inherited
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", string(raw), 409)
		request["lineage"].(map[string]any)["repo_inherited"] = inherited
		body["idempotency_key"] = "foreign"
		foreign := fmt.Sprintf("git:example.test/foreign/repo-%x", seed)
		request["git"].(map[string]any)["repository"] = map[string]any{"key": foreign}
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", string(raw), 403)
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 8}); err != nil {
		t.Fatal(err)
	}
}
