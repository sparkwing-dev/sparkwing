package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func successfulExpansionFixture(t *testing.T) (*Tenant, ExpansionSubmission) {
	t.Helper()
	tenant, _, request := expansionFixture(t)
	request.Failure = nil
	request.Body = []byte(`{"jobs":[{"id":"generated","steps":[{"id":"step"}],"groups":["group"],"deps":["job"],"retry":{"attempts":2}}]}`)
	return tenant, request
}

func TestExpansionSuccessCommitsTopologyAndExecutionSettings(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	accepted, err := tenant.AcceptExpansion(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State != ExpansionAccepted || accepted.BodyHash == "" || len(accepted.Nodes) != 1 || accepted.Nodes[0].SpecHash == "" {
		t.Fatalf("accepted = %+v", accepted)
	}
	node := accepted.Nodes[0]
	if node.Ref.NodeID != "generated" || node.StateSeq != 0 || node.Status != StatusPending || node.Execution.Retry.Value != 2 {
		t.Fatalf("generated job = %+v", node)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := snapshot.Scheduling()
	if err != nil || len(graph.Expansions) != 1 || !reflect.DeepEqual(graph.Expansions[0].Members, []string{"generated"}) {
		t.Fatalf("projection = %+v, %v", graph, err)
	}
	request.Body = []byte(` { "jobs": [ { "deps": ["job"], "groups": ["group"], "id": "generated", "steps": [{"id":"step"}], "retry":{"attempts":2} } ] } `)
	replay, err := tenant.AcceptExpansion(t.Context(), request)
	if err != nil || !replay.Replayed || replay.BodyHash != accepted.BodyHash || len(replay.Nodes) != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	request.Body = []byte(`{"jobs":[]}`)
	if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
		t.Fatal("changed expansion replay accepted")
	}
}

func TestExpansionEmptySuccessPreservesEmptyGroup(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	request.Body = []byte(`{"jobs":[]}`)
	accepted, err := tenant.AcceptExpansion(t.Context(), request)
	if err != nil || accepted.State != ExpansionAccepted || len(accepted.Nodes) != 0 {
		t.Fatalf("empty expansion = %+v, %v", accepted, err)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := snapshot.Scheduling()
	if err != nil || len(graph.Expansions) != 1 || graph.Expansions[0].State != "accepted" || len(graph.Expansions[0].Members) != 0 {
		t.Fatalf("empty group = %+v, %v", graph, err)
	}
}

func TestExpansionRefusalRollsBackAllJobsAndBarrier(t *testing.T) {
	for _, problem := range []string{"duplicate_job", "wrong_group", "missing_jobs", "null_jobs", "secret_identifier"} {
		t.Run(problem, func(t *testing.T) {
			tenant, request := successfulExpansionFixture(t)
			switch problem {
			case "duplicate_job":
				request.Body = []byte(`{"jobs":[{"id":"first","steps":[{"id":"step"}],"groups":["group"]},{"id":"job","steps":[{"id":"step"}],"groups":["group"]}]}`)
			case "wrong_group":
				request.Body = []byte(`{"jobs":[{"id":"first","steps":[{"id":"step"}],"groups":["group"]},{"id":"second","steps":[{"id":"step"}],"groups":["foreign"]}]}`)
			case "missing_jobs":
				request.Body = []byte(`{}`)
			case "null_jobs":
				request.Body = []byte(`{"jobs":null}`)
			case "secret_identifier":
				request.Body = []byte(`{"jobs":[{"id":"job-42","steps":[{"id":"step"}],"groups":["group"]}]}`)
				var spec runspec.Spec
				spec.Declarations.Version = 1
				spec.Request.Request.Pipeline = "p"
				spec.Resolved.SecretArgs = []string{"token"}
				spec.Resolved.Args = map[string]string{"token": "42"}
				raw, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=? WHERE id='run'`, raw)
			}
			if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
				t.Fatal("invalid expansion accepted")
			}
			var count int
			if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE run_id='run'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("partial insertion: %d, %v", count, err)
			}
			node, err := tenant.ReadNode(t.Context(), request.Source)
			if err != nil || node.Expansion != ExpansionExpected {
				t.Fatalf("barrier changed: %s, %v", node.Expansion, err)
			}
		})
	}
}

type fakePlanSecretOpener func(context.Context, Team, Secret) (string, error)

func (f fakePlanSecretOpener) OpenPlanSecret(ctx context.Context, team Team, secret Secret) (string, error) {
	return f(ctx, team, secret)
}

func TestExpansionChecksStoredSecretValuesAndPools(t *testing.T) {
	for _, mode := range []string{"allowed", "missing_requirement", "ungranted_pool", "here", "no_opener", "decrypt_error"} {
		t.Run(mode, func(t *testing.T) {
			tenant, request := successfulExpansionFixture(t)
			var spec runspec.Spec
			spec.Declarations.Version = 1
			spec.Request.Request.Pipeline = "p"
			spec.Request.Provenance = assertedProvenance(t, "p", "", "")
			spec.Request.AllowedPools = []string{"prod"}
			spec.Resolved.SecretNames = []string{"token"}
			if mode == "ungranted_pool" {
				spec.Request.AllowedPools = nil
			}
			if mode == "here" {
				spec.Request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
			}
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=? WHERE id='run'`, raw)
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,pipeline,shared,only_on) VALUES ('a','token','ciphertext','account',1,1,1,'p',0,'prod'),('b','token','other-ciphertext','other',1,1,1,'p',0,'prod')`)
			if mode != "no_opener" {
				tenant.s.planSecretOpener = fakePlanSecretOpener(func(_ context.Context, team Team, secret Secret) (string, error) {
					if team != "a" || secret.Value != "ciphertext" || secret.Pipeline != "p" {
						t.Fatalf("secret scope = %q, %+v", team, secret)
					}
					if mode == "decrypt_error" {
						return "", errors.New("private diagnostic")
					}
					return "42", nil
				})
			}
			if mode != "missing_requirement" {
				request.Body = []byte(`{"jobs":[{"id":"generated","steps":[{"id":"step"}],"groups":["group"],"requires":["pool=prod"]}]}`)
			}
			_, err = tenant.AcceptExpansion(t.Context(), request)
			if mode == "allowed" && err != nil || mode != "allowed" && err == nil {
				t.Fatalf("acceptance = %v", err)
			}
			if mode != "allowed" {
				node, readErr := tenant.ReadNode(t.Context(), request.Source)
				if readErr != nil || node.Expansion != ExpansionExpected {
					t.Fatalf("refused expansion changed barrier: %+v, %v", node, readErr)
				}
			}
		})
	}
}

func TestExpansionReplayRechecksLeaseAfterSecretDecryption(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	var run RunRow
	run, err := tenant.ReadRun(t.Context(), string(request.Source.RunID))
	if err != nil {
		t.Fatal(err)
	}
	run.Spec.Resolved.SecretNames = []string{"clock-secret"}
	raw, err := json.Marshal(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=? WHERE id='run'`, raw)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,pipeline,shared,only_on) VALUES ('a','clock-secret','ciphertext','account',1,1,1,'p',0,'')`)
	tenant.s.planSecretOpener = fakePlanSecretOpener(func(context.Context, Team, Secret) (string, error) {
		expired := tenant.s.now().Add(time.Hour)
		tenant.s.clock = func() time.Time { return expired }
		return "clock-secret-value", nil
	})
	if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
		t.Fatal("replayed through expired owner")
	}
}
