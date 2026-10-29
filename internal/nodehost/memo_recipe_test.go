package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
)

func typescriptMemoPlan(t *testing.T, declarations string) nodeproto.PlanDoc {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for SDK parity")
	}
	module, err := filepath.Abs("../../sdk/typescript/src/index.ts")
	if err != nil {
		t.Fatal(err)
	}
	script := `import { Plan, compose, files, repoFiles, ignore, tree, env, constant, parts } from ` + string(mustMemoJSON(t, (&url.URL{Scheme: "file", Path: filepath.ToSlash(module)}).String())) + `;
 const plan = new Plan();
 ` + declarations + `
 console.log(JSON.stringify(plan.toDoc()));`
	raw, err := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	var doc nodeproto.PlanDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestMemoRecipeGoTypeScriptPipelinesUseExecutingHost(t *testing.T) {
	ts := typescriptMemoPlan(t, `plan.job("job", () => {}).memo(compose(files("**/*.txt"), repoFiles(ignore("cache/")), tree("tree"), env("MODE", "ABSENT"), constant("v1"), parts({z:2,a:["<秘密>",true]},3)));`)
	plan := sparkwing.NewPlan()
	recipe := inputs.Compose(inputs.Files("**/*.txt"), inputs.RepoFiles(inputs.Ignore("cache/")), inputs.Tree("tree"), inputs.Env("MODE", "ABSENT"), inputs.Const("v1"), inputs.Parts(map[string]any{"a": []any{"<秘密>", true}, "z": 2}, 3))
	sparkwing.Job(plan, "job", func(context.Context) error { return nil }).Memo(recipe)
	goJob := sdkserve.DefaultOperations.Document(plan).Jobs[0]
	for _, job := range []nodeproto.JobSpec{goJob, ts.Jobs[0]} {
		if job.Closures.MemoKey != "" || job.Memo == nil || job.Memo.Input == nil {
			t.Fatalf("SDK computed a memo key: %+v", job)
		}
	}
	workspace := createTestRepository(t, map[string]string{"a.txt": "one", "tree/b.txt": "two", "cache/data": "ignored", "nested/cache/data": "included"})
	host, input, _, _, session, _ := executorMemoFixture(t, nil)
	input.Entry = &source.Entry{Directory: workspace}
	// The host's ambient value must never substitute for the job's declared environment.
	t.Setenv("MODE", "ambient")
	effective := []string{"MODE=effective"}
	host.deps.memoEnvironment = func(context.Context, NodeInput) (procenv.Env, error) {
		return procenv.Build(procenv.Attempt, procenv.Base{Local: true, Captured: append([]string{"PATH=" + os.Getenv("PATH")}, effective...)}, procenv.Runtime{})
	}
	key := func(job nodeproto.JobSpec) string {
		t.Helper()
		key, err := host.memoKey(t.Context(), session, input, job)
		if err != nil || key == "" {
			t.Fatalf("memo key = %q, %v", key, err)
		}
		return key
	}
	before := key(goJob)
	if before != key(ts.Jobs[0]) {
		t.Fatal("Go and TypeScript recipes disagree")
	}
	effective = []string{"MODE=changed"}
	if before == key(goJob) || key(goJob) != key(ts.Jobs[0]) {
		t.Fatal("effective environment did not change both keys")
	}
	effective = []string{"MODE=effective"}
	writeAll(t, workspace, map[string]string{"a.txt": "changed"})
	if before == key(goJob) || key(goJob) != key(ts.Jobs[0]) {
		t.Fatal("file edit did not change both keys")
	}
	if err := os.Remove(filepath.Join(workspace, "a.txt")); err != nil {
		t.Fatal(err)
	}
	for _, job := range []nodeproto.JobSpec{goJob, ts.Jobs[0]} {
		key, err := host.memoKey(t.Context(), session, input, job)
		if key != "" || !errors.Is(err, os.ErrNotExist) || host.failure(err).Failure.Error.Code != apierr.Internal {
			t.Fatalf("filesystem error became key %q, %v", key, err)
		}
	}
}

func mustMemoJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMemoEnvironmentIncludesStartupAndBody(t *testing.T) {
	env, _, err := projectJobEnvironment(procenv.StepTool, map[string]nodeproto.EnvValue{
		"START": {Value: "startup", Startup: true}, "MODE": {Value: "body"},
	}, procenv.Runtime{}, false, nil, func(kind procenv.Kind, runtime procenv.Runtime) (procenv.Env, error) {
		return procenv.Build(kind, procenv.Base{Local: true, Captured: []string{"MODE=captured", "PATH=/bin"}}, runtime)
	})
	if err != nil {
		t.Fatal(err)
	}
	if env.Lookup("START") != "startup" || env.Lookup("MODE") != "body" {
		t.Fatal("memo environment differs from job tools")
	}
}

func TestMemoCallbackCanonicalJSONAndKeyInvalidation(t *testing.T) {
	first, err := memoValueKey(json.RawMessage(`{"z":1,"a":[2.0,"<秘密>"]}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := memoValueKey(json.RawMessage(`{"a":[2e0,"\u003c秘密\u003e"],"z":1.0}`))
	if err != nil || first != second {
		t.Fatalf("canonical callback keys differ: %s %s %v", first, second, err)
	}
	host, input, _, job, session, _ := executorMemoFixture(t, nil)
	key, err := host.memoKey(t.Context(), session, input, job)
	if err != nil {
		t.Fatal(err)
	}
	// The memo version domain separates these keys from a bare canonical hash.
	old, _ := runspec.CanonicalHash("key")
	if key == string(old) {
		t.Fatal("old memo namespace was reused")
	}
}

func TestMemoLiteralNumbersSurvivePlanRoundTrip(t *testing.T) {
	keys := map[string]bool{}
	for _, raw := range []string{`{"kind":"parts","parts":[9007199254740992]}`, `{"kind":"parts","parts":[9007199254740993]}`} {
		var recipe nodeproto.MemoInput
		if err := json.Unmarshal([]byte(raw), &recipe); err != nil {
			t.Fatal(err)
		}
		value, err := memoInputValue(t.Context(), recipe, "", procenv.Env{})
		if err != nil {
			t.Fatal(err)
		}
		key, err := memoValueKey(value)
		if err != nil || keys[key] {
			t.Fatalf("literal numbers share key %q: %v", key, err)
		}
		keys[key] = true
	}
}

func TestExecutorMemoRecipeErrorStopsBody(t *testing.T) {
	input, state, deps, processes := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
		job.Memo = &nodeproto.MemoSpec{Input: &nodeproto.MemoInput{Kind: "tree", Root: "missing"}}
	})
	input.Entry.Directory = createTestRepository(t, map[string]string{"file": "content"})
	deps.memoEnvironment = func(context.Context, NodeInput) (procenv.Env, error) {
		return procenv.Build(procenv.Attempt, procenv.Base{Local: true, Captured: os.Environ()}, procenv.Runtime{})
	}
	host, _, _ := executorPolicyHost(t, deps, input)
	if err := host.Execute(t.Context(), input); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filesystem error lost: %v", err)
	}
	if len(state.changes) != 2 {
		t.Fatalf("missing start or failure report: %+v", state.changes)
	}
	result := state.changes[1].Extra.Result
	if result.Failure == nil || result.Failure.Error.Code != apierr.Internal {
		t.Fatalf("recipe failed as author code: %+v", result)
	}
	for _, process := range *processes {
		for _, request := range process.requests {
			if request.Op == nodeproto.OpRunStep {
				t.Fatal("body ran after recipe error")
			}
		}
	}
}

func TestMemoConstantsSurviveTransportAndEmptyFailsAtHost(t *testing.T) {
	ts := typescriptMemoPlan(t, `
 plan.job("constant", () => {}).memo(constant("v1"));
 plan.job("empty", () => {}).memo(constant(""));
 plan.job("part", () => {}).memo(parts(""));`)
	plan := sparkwing.NewPlan()
	for i, recipe := range []sparkwing.MemoInput{inputs.Const("v1"), inputs.Const(""), inputs.Parts("")} {
		sparkwing.Job(plan, ts.Jobs[i].ID, func(context.Context) error { return nil }).Memo(recipe)
	}
	goDoc := sdkserve.DefaultOperations.Document(plan)
	for i, tsJob := range ts.Jobs {
		goJob := goDoc.Jobs[i]
		if string(mustMemoJSON(t, tsJob.Memo)) != string(mustMemoJSON(t, goJob.Memo)) {
			t.Fatalf("constant transport differs: Go %+v, TS %+v", goJob.Memo, tsJob.Memo)
		}
		for _, job := range []nodeproto.JobSpec{goJob, tsJob} {
			_, err := nodeproto.ValidatePlan(mustMemoJSON(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}), nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()})
			if job.ID == "empty" {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid {
					t.Fatalf("empty constant accepted by host: %v", err)
				}
			} else if err != nil {
				t.Fatalf("valid memo input refused by host: %v", err)
			}
		}
	}
}
