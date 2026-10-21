package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

type fakePlanContracts struct {
	validate func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error)
	resolve  func(settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error)
}

func (f fakePlanContracts) ValidatePlan(raw json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
	return f.validate(raw, ctx)
}

func (f fakePlanContracts) ResolveExpansion(in settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
	return f.resolve(in)
}

func (fakePlanContracts) CanonicalHash(value any) (runspec.Hash, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", err
	}
	hash, err := stateHash(document)
	return runspec.Hash(hash), err
}

func successfulExpansionFixture(t *testing.T) (*Tenant, ExpansionSubmission) {
	t.Helper()
	tenant, _, request := expansionFixture(t)
	request.Failure = nil
	request.Body = []byte(`{"jobs":[{"id":"generated","groups":["group"],"deps":["job"]}]}`)
	tenant.s.plans = fakePlanContracts{
		validate: func(raw json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
			if !ctx.Expansion || ctx.SourceJob != "job" || ctx.Placement != nodeproto.PlacementRunnerHosted || len(ctx.ExistingJobs) != 1 || ctx.ExistingJobs[0].ID != "job" || ctx.Secrets == nil {
				t.Fatalf("validation context = %+v", ctx)
			}
			var plan nodeproto.PlanDoc
			err := json.Unmarshal(raw, &plan)
			return plan, err
		},
		resolve: func(in settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
			if in.Spec.Request.Request.Pipeline != "p" {
				t.Fatalf("resolver lost accepted seal: %+v", in.Spec)
			}
			out := map[string]runspec.JobExecutionSettings{}
			for _, job := range in.Plan.Jobs {
				out[job.ID] = runspec.JobExecutionSettings{Retry: runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 2}}}
			}
			return out, nil
		},
	}
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
	request.Body = []byte(` { "jobs": [ { "deps": ["job"], "groups": ["group"], "id": "generated" } ] } `)
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
	for _, problem := range []string{"validator", "resolver", "missing_settings", "duplicate_job", "wrong_group", "missing_jobs", "null_jobs", "ancestor_lapse", "secret_identifier"} {
		t.Run(problem, func(t *testing.T) {
			tenant, request := successfulExpansionFixture(t)
			contracts := tenant.s.plans.(fakePlanContracts)
			switch problem {
			case "validator":
				contracts.validate = func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					return nodeproto.PlanDoc{}, errors.New("plan refused")
				}
			case "resolver":
				contracts.resolve = func(settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
					return nil, errors.New("ceiling exceeded")
				}
			case "missing_settings":
				contracts.resolve = func(settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
					return map[string]runspec.JobExecutionSettings{}, nil
				}
			case "duplicate_job":
				request.Body = []byte(`{"jobs":[{"id":"first","groups":["group"]},{"id":"job","groups":["group"]}]}`)
			case "wrong_group":
				request.Body = []byte(`{"jobs":[{"id":"first","groups":["group"]},{"id":"second","groups":["foreign"]}]}`)
			case "missing_jobs":
				request.Body = []byte(`{}`)
			case "null_jobs":
				request.Body = []byte(`{"jobs":null}`)
			case "ancestor_lapse":
				resolve := contracts.resolve
				contracts.resolve = func(in settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
					expired := tenant.s.now().Add(time.Hour)
					tenant.s.clock = func() time.Time { return expired }
					return resolve(in)
				}
			case "secret_identifier":
				contracts.validate = func(_ json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					if !ctx.Secrets.ContainsSecret("job-42") {
						t.Fatal("short registered secret missing from matcher")
					}
					return nodeproto.PlanDoc{}, authorityError(apierr.SecretInIdentifier, "identifier contains a secret")
				}
				var spec runspec.Spec
				spec.Request.Request.Pipeline = "p"
				spec.Resolved.SecretArgs = []string{"token"}
				spec.Resolved.Args = map[string]string{"token": "42"}
				raw, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=? WHERE id='run'`, raw)
			}
			tenant.s.plans = contracts
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
					return "4242", nil
				})
			}
			contracts := tenant.s.plans.(fakePlanContracts)
			validate := contracts.validate
			contracts.validate = func(raw json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
				if !ctx.Secrets.ContainsSecret("job-4242") {
					t.Fatal("stored secret not registered")
				}
				return validate(raw, ctx)
			}
			resolve := contracts.resolve
			contracts.resolve = func(in settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
				jobs, err := resolve(in)
				if mode != "missing_requirement" {
					job := jobs["generated"]
					job.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"pool=prod"}}}
					jobs["generated"] = job
				}
				return jobs, err
			}
			tenant.s.plans = contracts
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

func TestExpansionReplayRechecksLeaseAfterValidation(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	contracts := tenant.s.plans.(fakePlanContracts)
	validate := contracts.validate
	contracts.validate = func(raw json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
		expired := tenant.s.now().Add(time.Hour)
		tenant.s.clock = func() time.Time { return expired }
		return validate(raw, ctx)
	}
	tenant.s.plans = contracts
	if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
		t.Fatal("replayed through expired owner")
	}
}
