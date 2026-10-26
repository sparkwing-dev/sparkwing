package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPipelineLatestHTTPUsesNativeDeclaredJobReference(t *testing.T) {
	e, handler, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`,
		[]byte(`{"id":"job","pipeline_refs":[{"pipeline":"producer","job":"output"}]}`))
	now := time.Now().UTC()
	input := runspec.Request{
		Version: 1, Team: "a", Pipeline: "producer", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost,
		Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)}, Source: runspec.Source{Ref: strings.Repeat("a", 40)},
		RequestedArgs: map[string]string{"secret": "never-expose-secret", "public": "visible"},
		SecretArgs:    runspec.Optional[[]string]{Set: true, Value: []string{"secret"}},
	}
	request, err := runspec.SealRequest(input, runspec.IntakeFacts{RunID: "producer-run", AdmittedAt: now.Add(-time.Hour), Provenance: assertedProvenance(t, "producer", "", strings.Repeat("a", 40))})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,started_at,finished_at,log_retention_class,log_retention_days) VALUES ('a','producer-run',2,'producer','success',?,?,?,'gh:1','host','execute','account',?,?,?,'r30',30)`,
		encoded, request.Hash, request.AdmittedAt.UnixNano(), request.AdmittedAt.UnixNano(), request.AdmittedAt.UnixNano(), now.Add(-time.Minute).UnixNano())
	response := engineClaimHTTP(t, handler, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"]}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v %s", err, response.Body)
	}
	response = engineClaimHTTP(t, handler, award.Claim.Capability, "GET", "/api/v1/pipelines/producer/latest?max_age=2h", "", 200)
	var latest store.Run
	if err := json.Unmarshal(response.Body.Bytes(), &latest); err != nil || latest.ID != "producer-run" || latest.Args["secret"] != runspec.RedactedArgValue || latest.Args["public"] != "visible" || strings.Contains(response.Body.String(), "never-expose-secret") {
		t.Fatalf("latest: %v %+v %s", err, latest, response.Body)
	}
	engineClaimHTTP(t, handler, award.Claim.Capability, "GET", "/api/v1/pipelines/undeclared/latest", "", 403)
	engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job"}`))
	engineClaimHTTP(t, handler, award.Claim.Capability, "GET", "/api/v1/pipelines/producer/latest", "", 403)
}

func TestOwnerEvaluationLatestUsesDeclaredReferenceBeforeWorkAward(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, engine, capability.UnitTrigger)
	raw := nativeClaimPresentation(t, engine, owner)
	engineSQL(t, engine.Store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','job','pending',?,?)`, []byte(`{"id":"job","pipeline_refs":[{"pipeline":"producer","job":"output"}]}`), []byte(`{}`))
	server := New(engine.Store, nil)
	engineClaimHTTP(t, server.Handler(), raw, "GET", "/api/v1/pipelines/producer/latest", "", 404)
	engineClaimHTTP(t, server.Handler(), raw, "GET", "/api/v1/pipelines/undeclared/latest", "", 403)
}
