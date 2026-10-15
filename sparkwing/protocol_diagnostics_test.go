package sparkwing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

const (
	diagnosticDefault    = "default-秘密.*[x]$\"\\\n<&>"
	diagnosticJobDefault = "job-default-別é.*[y]$\"\\\n<&>"
)

type diagnosticInputs struct {
	First  string            `flag:"pipe-first" secret:"true" default:"default-秘密.*[x]$\"\\\n<&>"`
	Second string            `flag:"pipe-second" secret:"true" default:"default-秘密.*[x]$\"\\\n<&>-tail"`
	Check  int               `flag:"pipe-check"`
	Extra  map[string]string `flag:",extra"`
}

type diagnosticJobInputs struct {
	First  string `flag:"job-first" secret:"true" default:"job-default-別é.*[y]$\"\\\n<&>"`
	Second string `flag:"job-second" secret:"true" default:"job-default-別é.*[y]$\"\\\n<&>-tail"`
	Check  int    `flag:"job-check"`
}

type diagnosticInvalidJobInputs struct {
	First  string `flag:"job-first" secret:"true" required:"true" default:"job-default-別é.*[y]$\"\\\n<&>"`
	Second string `flag:"job-second" secret:"true" default:"job-default-別é.*[y]$\"\\\n<&>-tail"`
}

type diagnosticJob struct {
	WithArgs[diagnosticJobInputs]
	failure func() error
}

func (j *diagnosticJob) Work(w *Work) (*WorkStep, error) {
	if j.failure != nil {
		return nil, j.failure()
	}
	return Step(w, "run", func(context.Context) error { return nil }), nil
}

type diagnosticPipeline struct {
	declare func(*Plan) error
	help    func() string
}

func (p diagnosticPipeline) Plan(_ context.Context, plan *Plan, _ diagnosticInputs, _ RunContext) error {
	return p.declare(plan)
}

func (p diagnosticPipeline) Help() string {
	if p.help != nil {
		return p.help()
	}
	return "diagnostic fixture"
}

func diagnosticEncodings(value string) []string {
	raw, _ := json.Marshal(value)
	return []string{
		value, string(raw[1 : len(raw)-1]),
		strconv.Quote(value)[1 : len(strconv.Quote(value))-1],
		strconv.QuoteToASCII(value)[1 : len(strconv.QuoteToASCII(value))-1],
		url.QueryEscape(value), url.PathEscape(value),
		base64.StdEncoding.EncodeToString([]byte(value)),
		base64.RawStdEncoding.EncodeToString([]byte(value)),
		base64.URLEncoding.EncodeToString([]byte(value)),
		base64.RawURLEncoding.EncodeToString([]byte(value)),
	}
}

func FuzzPlanningDiagnosticsNeverExposeSecretArguments(f *testing.F) {
	for _, value := range []string{"", "0", "秘密é🙂", `.*+?^${}()|[]\`, "overlap-tail", "\"\\\n\t\x00<&>"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, generated string) {
		generated = "supplied-" + strings.ToValidUTF8(generated, "�")
		for _, scope := range []string{"pipeline", "job", "recovery", "approval-recovery"} {
			for _, stage := range []string{"describe", "help", "resolve", "plan", "work", "validate", "schema", "generate", "generate-resolve"} {
				if stage == "help" && scope != "pipeline" {
					continue
				}
				if (stage == "generate-resolve" || stage == "schema") && scope == "pipeline" {
					continue
				}
				for _, kind := range []string{"error", "canonical", "wrapped", "panic-error", "panic-string", "panic-object"} {
					t.Run(scope+"/"+stage+"/"+kind, func(t *testing.T) {
						isolateProtocolDiscoveryRegistry(t)
						secretDefault := diagnosticDefault
						if scope != "pipeline" {
							secretDefault = diagnosticJobDefault
						}
						values := []string{secretDefault, secretDefault + "-tail"}
						args := map[string]string{}
						if stage != "describe" && stage != "help" {
							values = append(values, generated, generated+"-tail")
							prefix := "pipe"
							if scope != "pipeline" {
								prefix = "job"
							}
							args[prefix+"-first"], args[prefix+"-second"] = generated, generated+"-tail"
							if stage == "resolve" || stage == "generate-resolve" {
								args[prefix+"-check"] = generated
							}
						}
						var encoded []string
						for _, value := range values {
							encoded = append(encoded, diagnosticEncodings(value)...)
						}
						payload := strings.Join(encoded, " | ")
						cause := &apierr.Error{
							Code: apierr.CapabilityUnsupported, Message: "refused: " + payload, Hint: payload,
							Details: map[string]any{"path": "jobs[0].steps[0].id", "count": 17, payload: []any{map[string]string{"value": payload}}},
						}
						fail := func() error {
							switch kind {
							case "canonical":
								return cause
							case "wrapped":
								return fmt.Errorf("wrapped: %w", cause)
							case "panic-error":
								panic(cause)
							case "panic-string":
								panic(payload)
							case "panic-object":
								panic(map[string]any{payload: []string{payload}})
							default:
								return errors.New(payload)
							}
						}
						declareJob := func(p *Plan, failure func() error) {
							if scope == "pipeline" {
								return
							}
							var j Workable = &diagnosticJob{failure: failure}
							if stage == "schema" {
								j = &protocolDiscoveryJob[diagnosticInvalidJobInputs]{}
							}
							if scope == "recovery" {
								Job(p, "root", func(context.Context) error { return nil }).OnFailure("recover", j)
							} else if scope == "approval-recovery" {
								p.Approval("approve", ApprovalConfig{}).OnFailure("recover", j)
							} else {
								Job(p, "job", j)
							}
						}
						Register[diagnosticInputs]("diagnostics", func() Pipeline[diagnosticInputs] {
							p := diagnosticPipeline{declare: func(p *Plan) error {
								if stage == "generate" || stage == "generate-resolve" {
									Job(p, "source", func(context.Context) error { return nil }).Generate(func(_ context.Context, expansion *Plan) error {
										declareJob(expansion, nil)
										if stage == "generate-resolve" {
											return nil
										}
										return fail()
									})
									return nil
								}
								if stage == "work" && scope != "pipeline" {
									declareJob(p, fail)
								}
								declareJob(p, nil)
								if stage == "resolve" {
									return nil
								}
								if stage == "validate" {
									Job(p, "invalid", func(context.Context) error { return nil }).Requires(payload).Inline()
									return nil
								}
								return fail()
							}}
							if stage == "help" {
								p.help = func() string { panic(cause) }
							}
							return p
						})
						var err error
						if stage == "describe" || stage == "help" {
							_, err = sdkserve.DefaultOperations.Describe(t.Context())
						} else {
							var built any
							var doc nodeproto.PlanDoc
							built, doc, err = sdkserve.DefaultOperations.Plan(t.Context(), "diagnostics", args, nodeproto.RunContext{})
							if stage == "generate" || stage == "generate-resolve" {
								if err != nil {
									t.Fatal(err)
								}
								result, evalErr := sdkserve.DefaultOperations.Eval(t.Context(), built, nodeproto.Request{Job: "source", Closure: doc.Jobs[0].Closures.Generate[0]})
								err = evalErr
								if result.Error != nil {
									err = result.Error
								}
							}
						}
						var canonical *apierr.Error
						if !errors.As(err, &canonical) {
							t.Fatalf("expected a canonical failure, got %v", err)
						}
						raw, marshalErr := json.Marshal(canonical)
						if marshalErr != nil {
							t.Fatal(marshalErr)
						}
						var wire any
						if err := json.Unmarshal(raw, &wire); err != nil {
							t.Fatal(err)
						}
						assertDiagnosticSecretsAbsent(t, wire, encoded)
						if stage != "resolve" && stage != "generate-resolve" && stage != "validate" && stage != "schema" && kind != "error" && kind != "panic-string" && kind != "panic-object" {
							if canonical.Code != cause.Code || canonical.Hint == "" || canonical.Details["path"] != "jobs[0].steps[0].id" {
								t.Errorf("canonical code, hint or field path lost: %+v", canonical)
							}
						}
						if cause.Message != "refused: "+payload || cause.Hint != payload || cause.Details["path"] != "jobs[0].steps[0].id" {
							t.Error("sanitizing mutated the author's error")
						}
					})
				}
			}
		}
	})
}

func assertDiagnosticSecretsAbsent(t *testing.T, value any, secrets []string) {
	t.Helper()
	switch value := value.(type) {
	case string:
		for _, secret := range secrets {
			if strings.Contains(value, secret) {
				t.Fatalf("secret encoding %q escaped the planning boundary in %q", secret, value)
			}
		}
	case []any:
		for _, child := range value {
			assertDiagnosticSecretsAbsent(t, child, secrets)
		}
	case map[string]any:
		for key, child := range value {
			assertDiagnosticSecretsAbsent(t, key, secrets)
			assertDiagnosticSecretsAbsent(t, child, secrets)
		}
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		assertDiagnosticSecretsAbsent(t, string(raw), secrets)
	}
}

func TestPlanningDiagnosticsMaskShortAndStructuredValues(t *testing.T) {
	for _, secret := range []string{"*", "***", "17", "true", "null", "\n"} {
		t.Run(strconv.Quote(secret), func(t *testing.T) {
			isolateProtocolDiscoveryRegistry(t)
			Register[secretInputs]("short", func() Pipeline[secretInputs] {
				return protocolDiscoveryPipeline[secretInputs]{declare: func(*Plan, secretInputs) error {
					return &apierr.Error{Code: apierr.PlanFailed, Message: secret, Hint: secret, Details: map[string]any{
						"path": "jobs[0].id", "values": []any{17, true, nil, secret},
					}}
				}}
			})
			_, _, err := sdkserve.DefaultOperations.Plan(t.Context(), "short", map[string]string{"token": secret}, nodeproto.RunContext{})
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.PlanFailed || failure.Details["path"] != "jobs[0].id" {
				t.Fatalf("lost canonical failure: %v", err)
			}
			raw, err := json.Marshal(failure)
			if err != nil {
				t.Fatal(err)
			}
			var wire any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			assertDiagnosticSecretsAbsent(t, wire, []string{secret})
		})
	}
}
