package nodeproto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type planSecrets []string

func (secrets planSecrets) ContainsSecret(identifier string) bool {
	for _, secret := range secrets {
		if secret != "" && strings.Contains(identifier, secret) {
			return true
		}
	}
	return false
}

func planContext() ValidationContext {
	return ValidationContext{Placement: PlacementLocal, Secrets: planSecrets{}}
}

func checkPlan(t *testing.T, raw string, context ValidationContext, code apierr.Code) PlanDoc {
	t.Helper()
	plan, err := ValidatePlan(json.RawMessage(raw), context)
	if code == "" {
		if err != nil {
			t.Fatalf("ValidatePlan: %v", err)
		}
		return plan
	}
	failure, ok := err.(*apierr.Error)
	if !ok || failure.Code != code {
		t.Fatalf("ValidatePlan error = %v, want %s", err, code)
	}
	return PlanDoc{}
}

func TestValidatePlanStrictJSON(t *testing.T) {
	valid := `{"jobs":[{"id":"build","steps":[{"id":"run"}]}]}`
	checkPlan(t, valid, planContext(), "")
	for name, raw := range map[string]string{
		"duplicate top level": `{"jobs":[],"jobs":[]}`,
		"duplicate nested":    `{"jobs":[{"id":"build","steps":[{"id":"run","id":"again"}]}]}`,
		"unknown top level":   `{"jobs":[],"run_id":"r"}`,
		"unknown nested":      `{"jobs":[{"id":"build","steps":[{"id":"run","is_result":true}]}]}`,
		"old wrapper":         `{"jobs":[],"modifiers":{}}`,
		"trailing object":     `{"jobs":[]} {}`,
		"null jobs":           `{"jobs":null}`,
		"null concurrency":    `{"jobs":[],"concurrency":null}`,
		"null steps":          `{"jobs":[{"id":"build","steps":null}]}`,
		"wrong type":          `{"jobs":"wrong"}`,
		"missing jobs":        `{}`,
		"missing retry count": `{"jobs":[{"id":"a","retry":{},"steps":[{"id":"s"}]}]}`,
		"missing cache keys":  `{"jobs":[{"id":"a","caches":[{"name":"deps"}],"steps":[{"id":"s"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) { checkPlan(t, raw, planContext(), apierr.PlanInvalid) })
	}
	checkPlan(t, `{"jobs":[],"concurrency":[]}`, planContext(), "")
	checkPlan(t, `{"jobs":[{"id":"a","retry":{"attempts":0},"caches":[{"name":"deps","key_files":[]}],"steps":[{"id":"s"}]}]}`, planContext(), "")
	if _, err := ValidatePlan(json.RawMessage(valid), ValidationContext{Placement: PlacementLocal}); err == nil {
		t.Fatal("missing matcher accepted")
	}
}

func TestConcurrencyMembershipHasIndependentCost(t *testing.T) {
	raw := `{"jobs":[
		{"id":"small","steps":[{"id":"run"}],"concurrency":[{"group":"pool","capacity":3,"cost":1,"scope":"run","on_limit":"queue"}]},
		{"id":"large","steps":[{"id":"run"}],"concurrency":[{"group":"pool","capacity":3,"cost":2,"scope":"run","on_limit":"queue"}]}
	]}`
	checkPlan(t, raw, planContext(), "")
}

func TestExpansionCannotDependOnItsOwnBarrierConsumer(t *testing.T) {
	base := checkPlan(t, `{"jobs":[
		{"id":"source","steps":[{"id":"run"}],"closures":{"generate":["generator"]}},
		{"id":"consumer","steps":[{"id":"run"}],"needs_generated":["generator"]}
	]}`, planContext(), "")
	ctx := planContext()
	ctx.Expansion, ctx.SourceJob, ctx.ExistingJobs = true, "source", base.Jobs
	checkPlan(t, `{"jobs":[{"id":"child","steps":[{"id":"run"}],"groups":["generator"],"deps":["consumer"]}]}`, ctx, apierr.PlanInvalid)
	checkPlan(t, `{"jobs":[{"id":"child","steps":[{"id":"run"}],"groups":["generator"],"deps":["source"]}]}`, ctx, "")

	base.Jobs = append(base.Jobs, JobSpec{ID: "child", Steps: []StepSpec{{ID: "run"}}, Groups: []string{"generator"}, Needs: []string{"consumer"}})
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	checkPlan(t, string(raw), planContext(), apierr.PlanInvalid)
}

func TestValidatePlanGraph(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate job":             `{"jobs":[{"id":"a","steps":[{"id":"s"}]},{"id":"a","steps":[{"id":"s"}]}]}`,
		"missing required":          `{"jobs":[{"id":"a","deps":["missing"],"steps":[{"id":"s"}]}]}`,
		"self edge":                 `{"jobs":[{"id":"a","deps":["a"],"steps":[{"id":"s"}]}]}`,
		"cycle":                     `{"jobs":[{"id":"a","deps":["b"],"steps":[{"id":"s"}]},{"id":"b","deps":["a"],"steps":[{"id":"s"}]}]}`,
		"missing generator":         `{"jobs":[{"id":"a","needs_generated":["nope"],"steps":[{"id":"s"}]}]}`,
		"duplicate closure":         `{"jobs":[{"id":"a","closures":{"generate":["g"]},"steps":[{"id":"s","closures":{"skip_if":["g"]}}]}]}`,
		"missing recovery":          `{"jobs":[{"id":"a","on_failure_of":"missing","steps":[{"id":"s"}]}]}`,
		"missing artifact producer": `{"jobs":[{"id":"a","consumes":[{"producer":"missing"}],"steps":[{"id":"s"}]}]}`,
		"duplicate step":            `{"jobs":[{"id":"a","steps":[{"id":"s"},{"id":"s"}]}]}`,
		"step cycle":                `{"jobs":[{"id":"a","steps":[{"id":"s","needs":["t"]},{"id":"t","needs":["s"]}]}]}`,
		"cross job step":            `{"jobs":[{"id":"a","steps":[{"id":"s","needs":["t"]}]},{"id":"b","steps":[{"id":"t"}]}]}`,
		"missing result":            `{"jobs":[{"id":"a","result_step":"missing","steps":[{"id":"s"}]}]}`,
		"empty body":                `{"jobs":[{"id":"a","steps":[]}]}`,
		"approval body":             `{"jobs":[{"id":"a","approval":{},"steps":[{"id":"s"}]}]}`,
		"approval recovery":         `{"jobs":[{"id":"a","approval":{},"on_failure_of":"b"},{"id":"b","steps":[{"id":"s"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) { checkPlan(t, raw, planContext(), apierr.PlanInvalid) })
	}
	checkPlan(t, `{"jobs":[{"id":"a","deps_if_present":["absent"],"steps":[{"id":"s"}]},{"id":"gate","approval":{"timeout_ms":0,"on_timeout":"approve"}}]}`, planContext(), "")
}

func TestValidatePlanValuesAndCapabilities(t *testing.T) {
	for name, raw := range map[string]string{
		"failure policy":        `{"jobs":[{"id":"a","failure_policy":"collect-all","steps":[{"id":"s"}]}]}`,
		"concurrency scope":     `{"jobs":[],"concurrency":[{"group":"g","scope":"planet","capacity":1,"cost":1,"on_limit":"queue"}]}`,
		"negative retry":        `{"jobs":[{"id":"a","retry":{"attempts":-1},"steps":[{"id":"s"}]}]}`,
		"negative memory":       `{"jobs":[],"resources":{"memory_bytes":-1}}`,
		"memo key without memo": `{"jobs":[{"id":"a","closures":{"memo_key":"key"},"steps":[{"id":"s"}]}]}`,
		"memo without key":      `{"jobs":[{"id":"a","memo":{},"steps":[{"id":"s"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) { checkPlan(t, raw, planContext(), apierr.PlanInvalid) })
	}
	for _, placement := range []Placement{PlacementLocal, PlacementLocalTriggered, PlacementRunnerHosted, PlacementLauncher, PlacementHere, PlacementGitHubActions} {
		context := planContext()
		context.Placement = placement
		checkPlan(t, `{"jobs":[{"id":"a","approval":{},"closures":{"skip_if":["condition"]}}],"concurrency":[{"group":"g","scope":"box","capacity":1,"cost":1,"on_limit":"queue"}]}`, context, "")
	}
	context := planContext()
	context.Placement = "unknown"
	checkPlan(t, `{"jobs":[]}`, context, apierr.PlanInvalid)
}

func TestPlanCapabilityLaunchParity(t *testing.T) {
	rows := CapabilityTable()
	if len(rows) != 35 {
		t.Fatalf("capability rows = %d, want 35", len(rows))
	}
	seen := map[PlanCapability]bool{}
	for _, row := range rows {
		if seen[row.Feature] {
			t.Fatalf("duplicate capability %s", row.Feature)
		}
		seen[row.Feature] = true
		if !row.Local || !row.LocalTriggered || !row.RunnerHosted || !row.Launcher || !row.Here || !row.GitHubActions {
			t.Fatalf("launch placement gap in %s", row.Feature)
		}
	}
}

func TestValidatePlanPreservesExplicitZero(t *testing.T) {
	plan := checkPlan(t, `{"jobs":[{"id":"a","timeout_ms":0,"retry":{"attempts":0,"backoff_ms":0},"memo":{"ttl_ms":0},"closures":{"memo_key":"key"},"steps":[{"id":"s"}]}],"priority":0,"claim_wait_ms":0,"source":{"full_history":false,"depth":0}}`, planContext(), "")
	if plan.Priority == nil || *plan.Priority != 0 || plan.ClaimWaitMS == nil || *plan.ClaimWaitMS != 0 || plan.Source == nil || plan.Source.FullHistory == nil || *plan.Source.FullHistory || plan.Source.Depth == nil {
		t.Fatalf("explicit settings lost presence: %+v", plan)
	}
	job := plan.Jobs[0]
	if job.TimeoutMS == nil || job.Retry == nil || job.Retry.BackoffMS == nil || job.Memo == nil || job.Memo.TTLMS == nil {
		t.Fatalf("explicit job settings lost presence: %+v", job)
	}
}

func TestValidatePlanSecretsStayOutOfErrors(t *testing.T) {
	context := planContext()
	context.Secrets = planSecrets{"sëcret", ""}
	for name, raw := range map[string]string{
		"job":       `{"jobs":[{"id":"pre-sëcret-post","steps":[{"id":"s"}]}]}`,
		"step":      `{"jobs":[{"id":"a","steps":[{"id":"pre-sëcret-post"}]}]}`,
		"group":     `{"jobs":[{"id":"a","groups":["pre-sëcret-post"],"steps":[{"id":"s"}]}]}`,
		"approval":  `{"jobs":[{"id":"pre-sëcret-post","approval":{}}]}`,
		"reference": `{"jobs":[{"id":"a","deps_if_present":["pre-sëcret-post"],"steps":[{"id":"s"}]}]}`,
		"closure":   `{"jobs":[{"id":"a","closures":{"generate":["pre-sëcret-post"]},"steps":[{"id":"s"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidatePlan(json.RawMessage(raw), context)
			failure, ok := err.(*apierr.Error)
			if !ok || failure.Code != apierr.SecretInIdentifier {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(fmt.Sprint(failure), "sëcret") || strings.Contains(fmt.Sprint(failure.Details), "sëcret") {
				t.Fatalf("secret leaked: %v", failure)
			}
		})
	}
	checkPlan(t, `{"jobs":[{"id":"near-miss","steps":[{"id":"s"}]}]}`, context, "")
	_, err := ValidatePlan(json.RawMessage(`{"jobs":[],"pre-sëcret-post":true}`), context)
	if err == nil || strings.Contains(err.Error(), "sëcret") {
		t.Fatalf("unknown key leaked a secret or was accepted: %v", err)
	}
	context.Secrets = planSecrets{"jobs"}
	_, err = ValidatePlan(json.RawMessage(`{"jobs":[{"id":"","steps":[{"id":"s"}]}]}`), context)
	if err == nil || strings.Contains(err.Error(), "jobs") {
		t.Fatalf("static field path leaked a registered value: %v", err)
	}
}

func TestSecretIdentifiers(t *testing.T) {
	context := planContext()
	context.Secrets = planSecrets{"hunter22", "42"}
	rows := []struct {
		name, raw, field string
	}{
		{"node id", `{"jobs":[{"id":"a","steps":[{"id":"s"}]},{"id":"b","steps":[{"id":"s"}]},{"id":"deploy-hunter22","steps":[{"id":"s"}]}]}`, "jobs[2].id"},
		{"group", `{"jobs":[{"id":"a","groups":["x","g-hunter22"],"steps":[{"id":"s"}]}]}`, "jobs[0].groups[1]"},
		{"step id", `{"jobs":[{"id":"a","steps":[{"id":"s"},{"id":"hunter22"}]}]}`, "jobs[0].steps[1].id"},
		{"step group", `{"jobs":[{"id":"a","steps":[{"id":"s","groups":["hunter22"]}]}]}`, "jobs[0].steps[0].groups[0]"},
		{"concurrency group", `{"jobs":[{"id":"a","steps":[{"id":"s"}],"concurrency":[{"group":"q-hunter22","scope":"run","capacity":1,"cost":1,"on_limit":"queue"}]}]}`, "jobs[0].concurrency[0].group"},
		{"runs_on label", `{"jobs":[{"id":"a","requires":["hunter22"],"steps":[{"id":"s"}]}]}`, "jobs[0].requires[0]"},
		{"below the floor", `{"jobs":[{"id":"node-42","steps":[{"id":"s"}]}]}`, "jobs[0].id"},
		{"clean", `{"jobs":[{"id":"a","groups":["g"],"steps":[{"id":"s"}]}]}`, ""},
		{"approval id", `{"jobs":[{"id":"approve-hunter22","approval":{}}]}`, "jobs[0].id"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := ValidatePlan(json.RawMessage(row.raw), context)
			if row.field == "" {
				if err != nil {
					t.Fatalf("clean identifiers refused: %v", err)
				}
				return
			}
			failure, ok := err.(*apierr.Error)
			if !ok || failure.Code != apierr.SecretInIdentifier || failure.Details["field"] != row.field {
				t.Fatalf("error = %v; want field %s", err, row.field)
			}
			serialized, _ := json.Marshal(failure)
			if strings.Contains(string(serialized), "hunter22") || strings.Contains(string(serialized), "42") || strings.Contains(string(serialized), "deploy-") {
				t.Fatalf("secret or identifier leaked: %s", serialized)
			}
		})
	}
	expansion := context
	expansion.Expansion = true
	expansion.ExistingJobs = []JobSpec{{ID: "source", Steps: []StepSpec{{ID: "run"}}, Closures: ClosureSpec{Generate: []string{"fanout"}}}}
	expansion.SourceJob = "source"
	_, err := ValidatePlan(json.RawMessage(`{"jobs":[{"id":"generated-hunter22","groups":["fanout"],"steps":[{"id":"run"}]}]}`), expansion)
	failure, ok := err.(*apierr.Error)
	if !ok || failure.Code != apierr.SecretInIdentifier || failure.Details["field"] != "jobs[0].id" {
		t.Fatalf("expansion error = %v", err)
	}
}

func TestValidatePlanExpansion(t *testing.T) {
	source := JobSpec{ID: "source", Steps: []StepSpec{{ID: "run"}}, Closures: ClosureSpec{Generate: []string{"first", "second", "empty"}}}
	context := planContext()
	context.Expansion = true
	context.ExistingJobs = []JobSpec{source}
	context.SourceJob = source.ID
	makeJobs := func(first, second int) string {
		jobs := make([]JobSpec, 0, first+second)
		for group, count := range map[string]int{"first": first, "second": second} {
			for i := 0; i < count; i++ {
				jobs = append(jobs, JobSpec{ID: fmt.Sprintf("%s-%d", group, i), Groups: []string{group}, Steps: []StepSpec{{ID: "run"}}})
			}
		}
		raw, err := json.Marshal(PlanDoc{Jobs: jobs})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	checkPlan(t, makeJobs(256, 256), context, "")
	checkPlan(t, makeJobs(257, 0), context, apierr.PlanInvalid)
	checkPlan(t, `{"jobs":[{"id":"source","groups":["first"],"steps":[{"id":"run"}]}]}`, context, apierr.PlanInvalid)
	checkPlan(t, `{"jobs":[{"id":"child","groups":["first","second"],"steps":[{"id":"run"}]}]}`, context, apierr.PlanInvalid)
	checkPlan(t, `{"jobs":[{"id":"child","groups":["other"],"steps":[{"id":"run"}]}]}`, context, apierr.PlanInvalid)
	checkPlan(t, `{"jobs":[],"priority":0}`, context, apierr.PlanInvalid)
	checkPlan(t, `{"jobs":[]}`, context, "")
	cyclic := context
	cyclic.ExistingJobs = []JobSpec{{ID: "source", Steps: []StepSpec{{ID: "run"}}, NeedsIfPresent: []string{"child"}, Closures: ClosureSpec{Generate: []string{"first"}}}}
	checkPlan(t, `{"jobs":[{"id":"child","groups":["first"],"steps":[{"id":"run"}]}]}`, cyclic, apierr.PlanInvalid)
	initialJobs := make([]JobSpec, 257)
	for i := range initialJobs {
		initialJobs[i] = JobSpec{ID: fmt.Sprintf("job-%d", i), Steps: []StepSpec{{ID: "run"}}}
	}
	initialRaw, _ := json.Marshal(PlanDoc{Jobs: initialJobs})
	checkPlan(t, string(initialRaw), planContext(), "")
}

func TestValidatePlanRefusesCachePathsOutsideTheWorkspace(t *testing.T) {
	for _, path := range []string{"/etc/sparkwing/credentials", "../outside"} {
		checkPlan(t, `{"jobs":[{"id":"a","caches":[{"name":"deps","path":"`+path+`","key_files":["go.sum"]}],"steps":[{"id":"s"}]}]}`, planContext(), apierr.PlanInvalid)
	}
}

func TestValidatePlanChildTargets(t *testing.T) {
	plan := checkPlan(t, `{"jobs":[{"id":"release","children":[{"pipeline":"deploy"},{"pipeline":"publish","repository":"git:host/org/repo"}],"steps":[{"id":"run"}]}]}`, planContext(), "")
	if len(plan.Jobs[0].Children) != 2 || plan.Jobs[0].Children[1].Repository != "git:host/org/repo" {
		t.Fatalf("children = %+v", plan.Jobs[0].Children)
	}
	for _, children := range []string{`["deploy"]`, `[{"pipeline":""}]`} {
		raw := fmt.Sprintf(`{"jobs":[{"id":"release","children":%s,"steps":[{"id":"run"}]}]}`, children)
		if _, err := ValidatePlan(json.RawMessage(raw), planContext()); err == nil {
			t.Fatalf("accepted children %s", children)
		}
	}
}

func TestValidatePlanNormalizesRunnerLabels(t *testing.T) {
	plan := checkPlan(t, `{"jobs":[{"id":"build","requires":["", " gpu ", "gpu", "os=linux, os=macos", " "],"prefers":["", " fast ", "fast"],"steps":[{"id":"body"}]}]}`, planContext(), "")
	if got := strings.Join(plan.Jobs[0].Requires, ";"); got != "gpu;os=linux, os=macos" {
		t.Fatalf("normalized requirements = %q", got)
	}
	if got := strings.Join(plan.Jobs[0].Prefers, ";"); got != "fast" {
		t.Fatalf("normalized preferences = %q", got)
	}
}
