package sparkwing

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type protocolDiscoveryInputs struct {
	Repository string `flag:"repository" required:"true"`
	Target     string `flag:"target" default:"production"`
}

type protocolDiscoveryJobArgs struct {
	Count int    `flag:"count" required:"true"`
	Mode  string `flag:"mode" short:"m" default:"rolling" enum:"rolling,recreate"`
}

type protocolDiscoveryRecoveryArgs struct {
	Webhook string `flag:"webhook" required:"true"`
	Retries int    `flag:"retries" default:"2"`
}

type protocolDiscoveryJob[T any] struct {
	WithArgs[T]
	risk string
}

func (j *protocolDiscoveryJob[T]) Work(w *Work) (*WorkStep, error) {
	return Step(w, "apply", func(context.Context) error { return nil }).Risk(j.risk), nil
}

type protocolDiscoveryPipeline[T any] struct {
	declare func(*Plan, T) error
}

func (p protocolDiscoveryPipeline[T]) Plan(_ context.Context, plan *Plan, in T, _ RunContext) error {
	return p.declare(plan, in)
}

func isolateProtocolDiscoveryRegistry(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	prior := registry
	registry = map[string]*registration{}
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registry = prior
		registryMu.Unlock()
	})
}

func TestProtocolDescribeRequiredInputsPreserveJobMetadata(t *testing.T) {
	isolateProtocolDiscoveryRegistry(t)
	Register[protocolDiscoveryInputs]("deploy", func() Pipeline[protocolDiscoveryInputs] {
		return protocolDiscoveryPipeline[protocolDiscoveryInputs]{declare: func(p *Plan, in protocolDiscoveryInputs) error {
			if in.Target != "" {
				return errors.New("discovery must use zero values before host preparation")
			}
			Job(p, "deploy", &protocolDiscoveryJob[protocolDiscoveryJobArgs]{risk: "production"}).
				OnFailure("recover", &protocolDiscoveryJob[protocolDiscoveryRecoveryArgs]{risk: "destructive"})
			return nil
		}}
	})
	doc, err := (sdkOperations{}).Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pipelines) != 1 {
		t.Fatalf("pipelines = %+v", doc.Pipelines)
	}
	d := doc.Pipelines[0]
	if len(d.Args) != 6 {
		t.Fatalf("required pipeline input erased job arguments: %+v", d.Args)
	}
	byName := map[string]nodeproto.ArgumentDescription{}
	for _, arg := range d.Args {
		byName[arg.Name] = arg
	}
	if arg := byName["repository"]; !arg.Required || arg.JobID != "" {
		t.Errorf("pipeline argument = %+v", arg)
	}
	for _, name := range []string{"count", "webhook"} {
		wantOwner := "deploy"
		if name == "webhook" {
			wantOwner = "recover"
		}
		if arg := byName[name]; !arg.Required || arg.JobID != wantOwner {
			t.Errorf("--%s = %+v", name, arg)
		}
	}
	if arg := byName["mode"]; arg.JobID != "deploy" || arg.Short != "m" || arg.Default == nil || *arg.Default != "rolling" || !reflect.DeepEqual(arg.Enum, []string{"rolling", "recreate"}) {
		t.Errorf("ordinary job argument = %+v", arg)
	}
	if arg := byName["retries"]; arg.JobID != "recover" || arg.Default == nil || *arg.Default != "2" {
		t.Errorf("recovery argument = %+v", arg)
	}
	if !reflect.DeepEqual(d.Risks, []string{"destructive", "production"}) {
		t.Errorf("risks = %v", d.Risks)
	}
	wantSteps := []nodeproto.StepRisks{
		{JobID: "deploy", StepID: "apply", Labels: []string{"production"}},
		{JobID: "recover", StepID: "apply", Labels: []string{"destructive"}},
	}
	if !reflect.DeepEqual(d.RisksByStep, wantSteps) {
		t.Errorf("step risks = %+v, want %+v", d.RisksByStep, wantSteps)
	}
}

func TestProtocolDescribeReportsDiscoveryFailures(t *testing.T) {
	cause := errors.New("declaration failed")
	for _, kind := range []string{"error", "panic-error", "panic-string"} {
		t.Run(kind, func(t *testing.T) {
			isolateProtocolDiscoveryRegistry(t)
			Register[NoInputs]("broken", func() Pipeline[NoInputs] {
				return protocolDiscoveryPipeline[NoInputs]{declare: func(p *Plan, _ NoInputs) error {

					if kind == "panic-error" {
						panic(cause)
					}
					if kind == "panic-string" {
						panic(cause.Error())
					}
					if kind == "error" {
						return cause
					}
					return nil
				}}
			})
			Register[NoInputs]("healthy", func() Pipeline[NoInputs] {
				return protocolDiscoveryPipeline[NoInputs]{declare: func(*Plan, NoInputs) error { return nil }}
			})
			doc, err := (sdkOperations{}).Describe(t.Context())
			if err != nil || len(doc.Pipelines) != 2 {
				t.Fatalf("one pipeline failure blocked discovery: %+v: %v", doc, err)
			}
			raw, err := json.Marshal(doc.Pipelines[0])
			if err != nil {
				t.Fatal(err)
			}
			var broken struct {
				Error   *apierr.Error                 `json:"error"`
				Secrets []nodeproto.SecretDeclaration `json:"secrets"`
			}
			if err := json.Unmarshal(raw, &broken); err != nil {
				t.Fatal(err)
			}
			failure := broken.Error
			if failure == nil || failure.Code != apierr.PlanFailed || !strings.Contains(failure.Message, "broken") || len(broken.Secrets) != 0 {
				t.Fatalf("discovery published incomplete declarations or lost cause: %s", raw)
			}
			if !strings.Contains(failure.Message, cause.Error()) {
				t.Errorf("discovery lost its failure message: %v", failure)
			}
		})
	}
}

func TestProtocolDiscoveryInitializesExtraArgumentBag(t *testing.T) {
	type inputs struct {
		Extra map[string]string `flag:",extra"`
	}
	reg := buildRegistration("extra", func() Pipeline[inputs] {
		return protocolDiscoveryPipeline[inputs]{declare: func(_ *Plan, in inputs) error {
			in.Extra["declared"] = "value"
			return nil
		}}
	}, "test")
	if _, err := describePipeline(t.Context(), reg); err != nil {
		t.Fatal(err)
	}
}

type protocolDiscoveryHelpPanic struct{ failure any }

func (p protocolDiscoveryHelpPanic) Help() string { panic(p.failure) }

func FuzzStandaloneProtocolDescribeReturnsAuthorError(f *testing.F) {
	for _, secret := range []string{"", "prefix", "***", "\x00\"\\\n", "é秘密"} {
		f.Add(secret, "declaration refused")
	}
	f.Fuzz(func(t *testing.T, rawSecret, payload string) {
		first := "secret-" + hex.EncodeToString([]byte(rawSecret))
		second := first + "-tail"
		schema, err := parseInputsSchema(reflect.StructOf([]reflect.StructField{
			{Name: "First", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(`flag:"first" secret:"true" default:` + strconv.Quote(first))},
			{Name: "Second", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(`flag:"second" secret:"true" default:` + strconv.Quote(second))},
		}))
		if err != nil {
			t.Fatal(err)
		}
		cause := &apierr.Error{Code: apierr.PlanFailed, Message: fmt.Sprintf("%s: %s %s", payload, first, second), Hint: first, Details: map[string]any{"value": second}}
		for _, kind := range []string{"error", "wrapped-error", "panic-error", "panic-string", "provider-panic"} {
			reg := &registration{
				Name: "masked", Schema: schema,
				instance: func() any {
					if kind == "provider-panic" {
						return protocolDiscoveryHelpPanic{failure: cause}
					}
					return struct{}{}
				},
				discover: func(context.Context) (*Plan, error) {
					switch kind {
					case "panic-error":
						panic(cause)
					case "panic-string":
						panic(cause.Error())
					case "wrapped-error":
						return nil, fmt.Errorf("wrapped: %w", cause)
					default:
						return nil, cause
					}
				},
			}
			_, err := describePipeline(t.Context(), reg)
			if err == nil {
				t.Fatalf("%s returned success", kind)
			}
			serialized := err.Error()
			var structured *apierr.Error
			if errors.As(err, &structured) {
				raw, marshalErr := json.Marshal(structured)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				serialized += string(raw)
			}
			if !strings.Contains(serialized, first) || !strings.Contains(serialized, second) {
				t.Fatalf("%s lost the author error before host sanitization: %s", kind, serialized)
			}
		}
	})
}

type isolatedSecretPipeline struct{ stage, cause string }

func (p isolatedSecretPipeline) Secrets() any {
	if p.stage == "secrets" {
		panic(p.cause)
	}
	return &struct {
		Token string `sw:"PIPELINE_TOKEN"`
	}{}
}

func (p isolatedSecretPipeline) Plan(context.Context, *Plan, NoInputs, RunContext) error {
	if p.stage == "plan" {
		return errors.New(p.cause)
	}
	return nil
}

func FuzzDescribePublishesOnlyCompletePipelineSecrets(f *testing.F) {
	f.Add("deployment setting missing", false)
	f.Add("error\x00\n秘密", true)
	f.Fuzz(func(t *testing.T, cause string, first bool) {
		for _, stage := range []string{"factory", "secrets", "plan"} {
			isolateProtocolDiscoveryRegistry(t)
			broken := "z-broken"
			if first {
				broken = "a-broken"
			}
			instances := 0
			Register[NoInputs](broken, func() Pipeline[NoInputs] {
				instances++
				if stage == "factory" && instances > 1 {
					panic(cause)
				}
				return isolatedSecretPipeline{stage: stage, cause: cause}
			})
			Register[NoInputs]("healthy", func() Pipeline[NoInputs] { return isolatedSecretPipeline{} })
			doc, err := (sdkOperations{}).Describe(t.Context())
			if err != nil || len(doc.Pipelines) != 2 {
				t.Fatalf("pipeline failure blocked describe: %+v: %v", doc, err)
			}
			for _, entry := range doc.Pipelines {
				if entry.Name == broken {
					if entry.Error == nil || entry.Error.Code != apierr.PlanFailed || len(entry.Args) != 0 || len(entry.Secrets) != 0 {
						t.Fatalf("failed pipeline published partial declarations: %+v", entry)
					}
				} else if entry.Error != nil || len(entry.Secrets) != 1 || entry.Secrets[0].Name != "PIPELINE_TOKEN" {
					t.Fatalf("healthy pipeline lost its declarations: %+v", entry)
				}
			}
		}
	})
}
