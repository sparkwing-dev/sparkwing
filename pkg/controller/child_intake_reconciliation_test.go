package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func childIntakeFixture(t *testing.T) (*EngineController, http.Handler, string) {
	t.Helper()
	e, h, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim, capability.TriggersClaim})
	e.Store.SetIntakePolicy(childIntakePolicy{})
	engineSQL(t, e.Store, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
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
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, raw)
	claimed := engineClaimHTTP(t, h, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(claimed.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %s: %v", claimed.Body, err)
	}
	engineClaimHTTP(t, h, award.Claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	return e, h, award.Claim.Capability
}

func childIntakeBody(t *testing.T, pipeline string, git runspec.Git) string {
	t.Helper()
	body, err := json.Marshal(controllerwire.CreateRunRequest{IdempotencyKey: "child-" + pipeline, Request: runspec.Request{Version: 1, Team: "a", Pipeline: pipeline, Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController, Origin: runspec.OriginChild, Git: git, Lineage: runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

type childIntakePolicy struct{}

func (childIntakePolicy) ResolveIntake(_ context.Context, _ capability.AuthorityResolver, request store.IntakeRequest) (store.IntakePolicyFacts, error) {
	return store.IntakePolicyFacts{Options: &request.Request.Options, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
}

func TestClaimRun_ChildRunsInheritTheParentAndAnswerOnlyIt(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	private := engineClaimHTTP(t, h, claim, "GET", "/api/v1/runs/run/local-host/nodes/job/snapshot", "", 200)
	var snapshot store.RunSnapshot
	if err := json.Unmarshal(private.Body.Bytes(), &snapshot); err != nil || snapshot.Run.Request.Request.RequestedArgs["token"] != "s3cret" {
		t.Fatalf("private snapshot: %s: %v", private.Body, err)
	}
	reader, _, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "reader"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
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
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','sibling','pending',1,?,?)`, []byte(`{"id":"sibling"}`), []byte(`{}`))
	root := engineHTTPRoot(t, e, "a", "sibling-runner", []capability.Grant{capability.NodesClaim})
	response := engineClaimHTTP(t, h, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 200)
	var sibling controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &sibling); err != nil || sibling.Claim == nil || sibling.Claim.Binding.NodeID != "sibling" {
		t.Fatalf("sibling: %s: %v", response.Body, err)
	}
	var kind string
	if err := e.Store.DB().QueryRow(`SELECT unit_kind FROM triggers WHERE team='a' AND id=?`, created.RunID).Scan(&kind); err != nil || kind != "plan" {
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
	if err := e.Store.DB().QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("runs=%d: %v", count, err)
	}
	engineSQL(t, e.Store, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
	engineClaimHTTP(t, h, claim, "GET", path, "", 401)
}

func TestTeamBoundary_TriggerAncestryStaysInTheCallersTeam(t *testing.T) {
	property := func(seed uint32) bool {
		e, h, claim := childIntakeFixture(t)
		team, err := e.Store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		parent, err := team.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Store.AsOperator().CreateTeam(t.Context(), "foreign"); err != nil {
			t.Fatal(err)
		}
		private := fmt.Sprintf("private-pipeline-%x", seed)
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
		if err := e.Store.DB().QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return replies[0] == replies[1] && !strings.Contains(replies[0], private) && count == 1
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}
