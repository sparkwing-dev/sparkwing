package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestHostArgumentRulesAgreeForBothSDKs(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "argumentsfixture")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/argumentsfixture")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go declarations: %v\n%s", err, output)
	}
	goDoc, err := exec.CommandContext(t.Context(), binary).Output()
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("TypeScript argument conformance requires node")
	}
	tsDoc, err := exec.CommandContext(t.Context(), node, "../../sdk/typescript/test/fixtures/arguments-describe.mjs").Output()
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string]map[string][]nodeproto.JobSpec{}
	for sdk, command := range map[string]*exec.Cmd{
		"go":         exec.CommandContext(t.Context(), binary, "--generated"),
		"typescript": exec.CommandContext(t.Context(), node, "../../sdk/typescript/test/fixtures/arguments-describe.mjs", "--generated"),
	} {
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s generated declarations: %v: %s", sdk, err, output)
		}
		var jobs map[string][]nodeproto.JobSpec
		if err := json.Unmarshal(output, &jobs); err != nil {
			t.Fatal(err)
		}
		generated[sdk] = jobs
	}
	documents := map[string][]byte{"go": goDoc, "typescript": tsDoc}
	for sdk, raw := range documents {
		var doc nodeproto.DescribeDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, description := range doc.Pipelines {
			if description.Error != nil {
				t.Fatalf("%s SDK rejected declarations before host: %s: %v", sdk, description.Name, description.Error)
			}
		}
	}
	for sdk, command := range map[string]*exec.Cmd{
		"go":         exec.CommandContext(t.Context(), binary, "--conditional"),
		"typescript": exec.CommandContext(t.Context(), node, "../../sdk/typescript/test/fixtures/arguments-describe.mjs", "--conditional"),
	} {
		t.Run("undiscovered-secret/"+sdk, func(t *testing.T) {
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("conditional declarations: %v: %s", err, output)
			}
			var conditional struct {
				Description json.RawMessage   `json:"description"`
				Plan        nodeproto.PlanDoc `json:"plan"`
			}
			if err := json.Unmarshal(output, &conditional); err != nil {
				t.Fatal(err)
			}
			planner, input, fixture, _ := sdkPlanningFixture(t)
			input.Entrypoint = "conditional"
			input.Args = map[string]string{"enable": "true"}
			start := planner.Start
			planner.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, input, log)
				if err != nil {
					return nil, err
				}
				p := process.(*executorProcess)
				answer := p.answer
				p.plan = conditional.Plan
				p.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpDescribe {
						return conditional.Description, nil
					}
					if request.Op == nodeproto.OpRunStep || request.Op == nodeproto.OpEval {
						t.Fatal("undiscovered arguments reached execution")
					}
					return answer(ctx, request)
				}
				return p, nil
			}
			prepared, err := planner.Prepare(t.Context(), input, fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			_, err = PlanInitial(t.Context(), prepared.Input, prepared.Plan)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.InvalidArgs || failure.Message != "initial plan argument declarations differ from SDK describe" {
				t.Fatalf("conditional plan host error = %v", err)
			}
		})
	}
	for _, pipeline := range []string{"secret-empty", "secret-duplicate"} {
		_, _, err := (SDKPlanner{Masker: secrets.NewMasker()}).describe(tsDoc, PlanningProcessInput{Entrypoint: pipeline}, false)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError || failure.Message != "SDK describe has invalid secret declarations" {
			t.Fatalf("TypeScript host secret error = %v", err)
		}
	}
	for _, test := range []struct {
		name, pipeline string
		args           map[string]string
		message        string
	}{
		{"required", "args", nil, "argument --text is required"},
		{"unknown", "args", map[string]string{"text": "yes", "unknown": "bad"}, "no flag --unknown is declared"},
		{"bool", "args", map[string]string{"text": "yes", "bool": "yes"}, "argument --bool must be bool"},
		{"int", "args", map[string]string{"text": "yes", "int": "1.2"}, "argument --int must be int"},
		{"int64", "args", map[string]string{"text": "yes", "int64": "9223372036854775808"}, "argument --int64 must be int64"},
		{"float", "args", map[string]string{"text": "yes", "float": "bad"}, "argument --float must be float64"},
		{"nan", "args", map[string]string{"text": "yes", "float": "NaN"}, "argument --float must be float64"},
		{"infinity", "args", map[string]string{"text": "yes", "float": "+Inf"}, "argument --float must be float64"},
		{"duration", "args", map[string]string{"text": "yes", "duration": "later"}, "argument --duration must be duration"},
		{"enum", "args", map[string]string{"text": "yes", "list": "dev,other"}, "argument --list is not an allowed value"},
		{"scalar-whitespace", "args", map[string]string{"text": " yes "}, "argument --text is not an allowed value"},
		{"duplicate-name", "duplicate-name", nil, "SDK describe has ambiguous arguments"},
		{"duplicate-short", "duplicate-short", nil, "argument --two has an invalid or duplicate short name"},
		{"invalid-short", "invalid-short", nil, "argument --one has an invalid or duplicate short name"},
		{"required-empty", "required-empty", nil, "argument --value is required"},
		{"required-default", "required-default", nil, "argument --value cannot be required and have a default"},
		{"enum-type", "enum-type", nil, "argument --value: enum requires string or []string"},
		{"enum-anchor", "enum-anchor", nil, "argument --value: enum requires a default or required argument"},
		{"enum-default", "enum-default", nil, "argument --value is not an allowed value"},
		{"float-default", "float-default", nil, "argument --value must be float64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for sdk, raw := range documents {
				planner := SDKPlanner{Masker: secrets.NewMasker()}
				_, _, err := planner.describe(raw, PlanningProcessInput{Entrypoint: test.pipeline, Args: test.args}, false)
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.InvalidArgs || failure.Message != test.message {
					t.Fatalf("%s host error = %v; want invalid_args: %s", sdk, err, test.message)
				}
				if test.name == "unknown" {
					continue
				} // Unknown inputs were judged before generation.
				jobs := generated[sdk][test.pipeline]
				if len(jobs) != 1 || len(jobs[0].Arguments) == 0 {
					t.Fatalf("%s omitted generated declarations: %+v", sdk, jobs)
				}
				session := executorSession{rawArgs: test.args}
				err = session.prepareArguments(jobs[0])
				if !errors.As(err, &failure) || failure.Code != apierr.InvalidArgs || failure.Message != test.message {
					t.Fatalf("%s generated host error = %v; want invalid_args: %s", sdk, err, test.message)
				}
			}
		})
	}
	static := []nodeproto.ArgumentDescription{{Name: "taken", Type: "string"}, {Name: "known", Type: "string", JobID: "child"}}
	changed := []nodeproto.JobSpec{{ID: "child", Arguments: []nodeproto.ArgumentDescription{{Name: "taken", Type: "string", JobID: "child"}}}}
	if err := nodeproto.ValidateJobArgumentNamespace(static, changed); err == nil {
		t.Fatal("changed described job escaped the shared argument namespace")
	}
	for sdk, plans := range generated {
		job := plans["args"][0]
		for _, static := range [][]nodeproto.ArgumentDescription{nil, {{Name: "text", Type: "string"}}} {
			jobs := []nodeproto.JobSpec{job}
			if static == nil {
				sibling := job
				sibling.ID = "sibling"
				jobs = append(jobs, sibling)
			}
			err := nodeproto.ValidateJobArgumentNamespace(static, jobs)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.InvalidArgs || failure.Message != "SDK describe has ambiguous arguments" {
				t.Fatalf("%s generated namespace collision = %v", sdk, err)
			}
		}
	}
	for sdk, raw := range documents {
		planner := SDKPlanner{Masker: secrets.NewMasker()}
		metadata, _, err := planner.describe(raw, PlanningProcessInput{Entrypoint: "args"}, true)
		if err != nil || !reflect.DeepEqual(metadata.Args, map[string]string{"list": "dev,prod"}) {
			t.Fatalf("%s declaration inspection required invocation values or lost defaults: %v, %v", sdk, metadata.Args, err)
		}
		for _, test := range []struct{ args, want map[string]string }{
			{map[string]string{"text": "yes"}, map[string]string{"text": "yes", "list": "dev,prod"}},
			{map[string]string{"text": "yes", "list": " prod, , dev ", "float": "1.5", "duration": "-0", "empty": ""}, map[string]string{"text": "yes", "list": "prod,dev", "float": "1.5", "duration": "-0", "empty": ""}},
		} {
			prepared, _, err := planner.describe(raw, PlanningProcessInput{Entrypoint: "args", Args: test.args}, false)
			if err != nil || !reflect.DeepEqual(prepared.Args, test.want) {
				t.Fatalf("%s host arguments = %v, %v; want %v", sdk, prepared.Args, err, test.want)
			}
		}
	}
}

func TestHostRefusesInvalidSecretDeclarationsAfterDescribe(t *testing.T) {
	for _, declarations := range [][]nodeproto.SecretDeclaration{{{Name: ""}}, {{Name: "TOKEN"}, {Name: "TOKEN"}}} {
		raw, err := json.Marshal(nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "pipeline", Args: []nodeproto.ArgumentDescription{}, Secrets: declarations}}})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = (SDKPlanner{Masker: secrets.NewMasker()}).describe(raw, PlanningProcessInput{Entrypoint: "pipeline"}, false)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
			t.Fatalf("secret declarations accepted: %v", err)
		}
	}
}

func TestHostAnnotationRulesAgreeForBothSDKs(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("TypeScript annotation conformance requires node")
	}
	raw, err := exec.CommandContext(t.Context(), node, "../../sdk/typescript/test/fixtures/annotation-requests.mjs").Output()
	if err != nil {
		t.Fatal(err)
	}
	var tsBodies []json.RawMessage
	if err := json.Unmarshal(raw, &tsBodies); err != nil {
		t.Fatal(err)
	}
	annotations := []nodeproto.AnnotationRequest{{Message: "range", Line: 5, EndLine: 4}, {Message: "level", Level: "warning"}, {Message: "negative", Line: -1}}
	if len(tsBodies) != len(annotations) {
		t.Fatalf("TypeScript rejected invalid annotations before host: %s", raw)
	}
	routes, service, auth := serviceFixture(t)
	calls := 0
	service.Annotation = func(_ context.Context, _ capability.Authorized, _ nodeproto.AnnotationRequest) error {
		calls++
		return nil
	}
	for index, annotation := range annotations {
		goBody, err := sdkserve.EncodeServiceRequest(annotation)
		if err != nil {
			t.Fatal(err)
		}
		for sdk, body := range map[string][]byte{"go": goBody, "typescript": tsBodies[index]} {
			_, err := callService(t, routes, auth, "/node/v1/annotations", string(body))
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.BadRequest || failure.Message != "invalid annotation level or location" {
				t.Fatalf("%s host annotation error = %v", sdk, err)
			}
		}
	}
	if calls != 0 {
		t.Fatalf("invalid annotations reached persistence %d times", calls)
	}
}
