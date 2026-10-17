package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type localTestController struct {
	controllerwire.Controller
	create func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error)
	claim  func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
}

func (c localTestController) CreateRun(ctx context.Context, request controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
	return c.create(ctx, request)
}

func (c localTestController) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return c.claim(ctx, request)
}

func localTestAward(t *testing.T) controllerwire.ClaimedUnit {
	t.Helper()
	request, err := runspec.SealRequest(runspec.Request{
		Version: 1, Team: "team", Pipeline: "ci", Mode: runspec.IntakeExecute, Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost,
		Git: runspec.Git{Repository: runspec.Repository{Key: runspec.RepoKey("local:" + strings.Repeat("a", 64))}}, RequestedArgs: map[string]string{"password": "private-value"},
	}, runspec.IntakeFacts{RunID: "local-run", AdmittedAt: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return controllerwire.ClaimedUnit{
		Unit: capability.UnitTrigger, Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect,
		RunID: "local-run", UnitID: "owner-unit", Capability: "private-award", Request: request,
		Binding: capability.Binding{Kind: capability.BindingTrigger, RunID: "local-run", Generation: 7, Holder: "granted-holder"},
		Runner:  nodeproto.RunnerIdentity{Name: "local", Type: "local"}, LeaseExpiresAt: time.Unix(900, 0),
	}
}

func localRuntimeFor(t *testing.T, award controllerwire.ClaimedUnit, detached bool, observed *[]string) localRunDependencies {
	t.Helper()
	return localRunDependencies{version: "v1.2.3", resolve: func(context.Context, localRunOptions) (localRunRuntime, error) {
		*observed = append(*observed, "resolve")
		return localRunRuntime{
			follow: func(context.Context, string, string, io.Writer) error { return nil },
			prepare: func(_ context.Context, pipeline string, flags runFlags) (localRunPreparation, error) {
				*observed = append(*observed, "prepare")
				if pipeline != "ci" {
					t.Fatal("preparation lost the requested pipeline")
				}
				return localTestPreparation(t, award), nil
			},
			controller: localTestController{
				create: func(_ context.Context, request controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
					*observed = append(*observed, "create")
					if request.IdempotencyKey == "" || !reflect.DeepEqual(request.Request.RequestedArgs, award.Request.Request.RequestedArgs) {
						t.Fatal("intake changed the prepared request")
					}
					request.Request.Team = "team"
					sealed, err := runspec.SealRequest(request.Request, runspec.IntakeFacts{RunID: award.RunID})
					if err != nil {
						t.Fatal(err)
					}
					award.Request = sealed
					return controllerwire.CreateRunResponse{RunID: award.RunID, Number: 1, Request: sealed}, nil
				},
				claim: func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					*observed = append(*observed, "claim")
					if detached || request.RunID != award.RunID || request.RunnerVersion != "v1.2.3" || request.AvailableCores <= 0 || !reflect.DeepEqual(request.Kinds, []capability.UnitKind{capability.UnitTrigger}) {
						t.Fatalf("invalid local owner claim: %+v", request)
					}
					return controllerwire.ClaimResponse{Claim: &award}, nil
				},
			},
			bind: func(_ context.Context, got controllerwire.ClaimedUnit) (hereHostSession, error) {
				*observed = append(*observed, "bind")
				if !reflect.DeepEqual(got, award) {
					t.Fatal("native binder lost the awarded authority")
				}
				return hereHostSession{
					input: nodehost.RunInput{RunID: "unsealed-run", SealedRequest: json.RawMessage(`{"mutable":true}`), RequestHash: "unsealed-hash"},
					close: func() error { *observed = append(*observed, "host-close"); return nil },
					host: hereTestHost(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
						*observed = append(*observed, "host")
						var sealed runspec.Request
						if err := json.Unmarshal(input.SealedRequest, &sealed); err != nil {
							t.Fatal(err)
						}
						if input.RunID != award.RunID || input.RequestHash != string(award.Request.Hash) || !reflect.DeepEqual(sealed, award.Request.Request) || !reflect.DeepEqual(input.GrantedRunner, award.Runner) {
							t.Fatal("host received mutable inputs instead of the awarded seal")
						}
						return nodehost.RunCompletion{Outcome: "success"}, nil
					}),
				}, nil
			},
			close: func() error { *observed = append(*observed, "close"); return nil },
		}, nil
	}}
}

func TestLocalRunUsesSealedIntakeAndNamedOwnerClaim(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(fmt.Sprint(detached), func(t *testing.T) {
			var calls []string
			var out bytes.Buffer
			deps := localRuntimeFor(t, localTestAward(t), detached, &calls)
			flags, args := parseRunFlags([]string{fmt.Sprintf("--sw-detached=%t", detached), "-o=json", "--", "--password", "private-value"})
			err := runLocalCommand(t.Context(), "ci", flags, args, &out, deps)
			want := []string{"resolve", "prepare", "create", "claim", "bind", "host", "host-close", "close"}
			if detached {
				want = []string{"resolve", "prepare", "create", "close"}
				if out.String() != "{\"run_id\":\"local-run\",\"replayed\":false}\n" {
					t.Fatalf("receipt: %s", &out)
				}
			}
			if err != nil || !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v error=%v", calls, err)
			}
		})
	}
}

func TestLocalRunEnsuresControllerAfterPreparationBeforeIntake(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			var calls []string
			deps := localRuntimeFor(t, localTestAward(t), true, &calls)
			resolve := deps.resolve
			failure := errors.New("cannot start local controller: executable refused")
			deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
				runtime, err := resolve(ctx, options)
				runtime.ensureController = func(context.Context) error {
					calls = append(calls, "ensure")
					if failed {
						return failure
					}
					return nil
				}
				return runtime, err
			}
			err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", "private-value"}, io.Discard, deps)
			want := []string{"resolve", "prepare", "ensure", "create", "close"}
			if failed {
				want = []string{"resolve", "prepare", "ensure", "close"}
				if !errors.Is(err, failure) {
					t.Fatalf("startup failure: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v, want %v", calls, want)
			}
		})
	}
}

func TestLocalForegroundRunReportsCommittedOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			var calls []string
			deps := localRuntimeFor(t, localTestAward(t), false, &calls)
			resolve := deps.resolve
			deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
				runtime, err := resolve(ctx, options)
				bind := runtime.bind
				runtime.bind = func(ctx context.Context, award controllerwire.ClaimedUnit) (hereHostSession, error) {
					session, err := bind(ctx, award)
					session.host = hereTestHost(func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error) {
						return nodehost.RunCompletion{Outcome: outcome}, nil
					})
					return session, err
				}
				return runtime, err
			}
			var out bytes.Buffer
			flags, args := parseRunFlags([]string{"--sw-output=json", "--", "--password=private-value"})
			err := runLocalCommand(t.Context(), "ci", flags, args, &out, deps)
			if (err == nil) != (outcome == "success") {
				t.Fatalf("outcome=%s error=%v", outcome, err)
			}
			var terminal struct {
				Event string `json:"event"`
				Attrs struct {
					RunID    string `json:"run_id"`
					Pipeline string `json:"pipeline"`
					Status   string `json:"status"`
				} `json:"attrs"`
			}
			decoder := json.NewDecoder(&out)
			if err := decoder.Decode(&terminal); err != nil || terminal.Event != "run_start" {
				t.Fatalf("missing start record: %+v %v", terminal, err)
			}
			if err := decoder.Decode(&terminal); err != nil {
				t.Fatal(err)
			}
			if terminal.Event != "run_finish" || terminal.Attrs.RunID != "local-run" || terminal.Attrs.Pipeline != "ci" || terminal.Attrs.Status != outcome {
				t.Fatalf("missing committed completion: %+v", terminal)
			}
		})
	}
}

func TestLocalRunNoUpdateReachesPreparation(t *testing.T) {
	var calls []string
	flags, args := parseRunFlags([]string{"--sw-no-update", "--", "--password=private-value"})
	deps := localRuntimeFor(t, localTestAward(t), false, &calls)
	resolve := deps.resolve
	deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, options)
		prepare := runtime.prepare
		runtime.prepare = func(ctx context.Context, pipeline string, flags runFlags) (localRunPreparation, error) {
			if !flags.noUpdate {
				t.Fatal("preparation lost --sw-no-update")
			}
			return prepare(ctx, pipeline, flags)
		}
		return runtime, err
	}
	if err := runLocalCommand(t.Context(), "ci", flags, args, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
}

func TestLocalForegroundRunOutputFailuresStayVisible(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		for _, at := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", format, at), func(t *testing.T) {
				var calls []string
				out := localFailingEventWriter{at: at}
				flags, args := parseRunFlags([]string{"--sw-output=" + format, "--", "--password=private-value"})
				err := runLocalCommand(t.Context(), "ci", flags, args, &out, localRuntimeFor(t, localTestAward(t), false, &calls))
				if err == nil || !strings.Contains(err.Error(), "closed output") || !strings.Contains(err.Error(), "local-run") {
					t.Fatalf("output failure was hidden: %v", err)
				}
				if at == 2 && !strings.Contains(out.String(), "local-run") {
					t.Fatal("start record lost admitted identity")
				}
			})
		}
	}
}

type localFailingEventWriter struct {
	bytes.Buffer
	at, writes int
}

func (w *localFailingEventWriter) Write(body []byte) (int, error) {
	w.writes++
	if w.writes == w.at {
		return 0, errors.New("closed output")
	}
	return w.Buffer.Write(body)
}

func TestLocalRunRejectsUnsupportedSelectionsBeforeAnyEffect(t *testing.T) {
	for _, flags := range []runFlags{{detached: true, index: "/index"}} {
		deps := localRunDependencies{resolve: func(context.Context, localRunOptions) (localRunRuntime, error) {
			t.Fatal("unsupported input reached native composition")
			return localRunRuntime{}, nil
		}}
		err := runLocalCommand(t.Context(), "ci", flags, nil, io.Discard, deps)
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.CapabilityUnsupported {
			t.Fatalf("unrepresented selection: %v", err)
		}
	}
}

func TestLocalIntakeCannotSwitchPipelineOrDispatchPurpose(t *testing.T) {
	award := localTestAward(t)
	property := func(pipeline string, mode, origin, dispatch uint8) bool {
		request := award.Request.Request
		request.Pipeline = pipeline
		request.Mode = []runspec.IntakeMode{runspec.IntakeExecute, runspec.IntakePublish}[mode%2]
		request.Origin = []runspec.Origin{runspec.OriginLocal, runspec.OriginTrigger}[origin%2]
		request.Dispatch = []runspec.Dispatch{runspec.DispatchHost, runspec.DispatchController}[dispatch%2]
		if request.Pipeline == "" {
			request.Pipeline = "other"
		}
		sealed, err := runspec.SealRequest(request, runspec.IntakeFacts{RunID: award.RunID})
		if err != nil {
			return false
		}
		var calls []string
		deps := localRuntimeFor(t, award, true, &calls)
		resolve := deps.resolve
		deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
			runtime, err := resolve(ctx, opts)
			runtime.controller = localTestController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
				return controllerwire.CreateRunResponse{RunID: award.RunID, Number: 1, Request: sealed}, nil
			}}
			return runtime, err
		}
		err = runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", "private-value"}, io.Discard, deps)
		valid := request.Pipeline == "ci" && request.Mode == runspec.IntakeExecute && request.Origin == runspec.OriginLocal && request.Dispatch == runspec.DispatchHost
		return (err == nil) == valid
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRunMasksEvenARejectedIntakeBearer(t *testing.T) {
	award := localTestAward(t)
	var calls []string
	deps := localRuntimeFor(t, award, true, &calls)
	resolve := deps.resolve
	deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, opts)
		runtime.controller = localTestController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			return controllerwire.CreateRunResponse{HereBearer: "unexpected-private-bearer"}, errors.New("malformed reply exposes unexpected-private-bearer")
		}}
		return runtime, err
	}
	err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", "private-value"}, io.Discard, deps)
	if err == nil || strings.Contains(err.Error(), "unexpected-private-bearer") {
		t.Fatalf("rejected response leaked private material: %v", err)
	}
}

func localTestPreparation(t *testing.T, award controllerwire.ClaimedUnit) localRunPreparation {
	t.Helper()
	env, err := runspec.EnvironmentDigest(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	return localRunPreparation{
		identity:  clisurface.PipelineIdentity{Name: "ci", Entrypoint: "CI"},
		described: []nodeproto.PipelineDescription{{Name: "ci", Args: []nodeproto.ArgumentDescription{{Name: "password", Type: "string", Secret: true}}}},
		git:       award.Request.Request.Git, source: runspec.Source{Entrypoint: "CI"}, environment: env,
		settings: settings.Inputs{Context: settings.Context{NumCPU: 4}},
	}
}

func TestParsedLocalOutputSurvivesIntakeAndReceipt(t *testing.T) {
	for _, option := range [][]string{{"-o", "plain"}, {"--output=plain"}, {"--sw-output=plain"}, nil} {
		t.Run(strings.Join(option, " "), func(t *testing.T) {
			t.Setenv("SPARKWING_LOG_FORMAT", "plain")
			flags, args := parseRunFlags(append(append([]string{"--sw-detached"}, option...), "--", "--password=private-value"))
			var calls []string
			deps := localRuntimeFor(t, localTestAward(t), true, &calls)
			resolve := deps.resolve
			deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
				runtime, err := resolve(ctx, opts)
				create := runtime.controller.(localTestController).create
				runtime.controller = localTestController{create: func(ctx context.Context, request controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
					wantLayer := runspec.LayerFlag
					if option == nil {
						wantLayer = runspec.LayerEnv
					}
					if output := request.Request.Options.Output; output.Value != "plain" || output.Origin.Layer != wantLayer {
						t.Errorf("sealed output = %+v, want plain from %s", output, wantLayer)
					}
					return create(ctx, request)
				}}
				return runtime, err
			}
			var out bytes.Buffer
			if err := runLocalCommand(t.Context(), "ci", flags, args, &out, deps); err != nil || out.String() != "local-run\n" {
				t.Fatalf("receipt = %q, error = %v", &out, err)
			}
		})
	}
}

func TestLocalPreparationRetainsC4PresenceAndMachineDefaults(t *testing.T) {
	input := localTestPreparation(t, localTestAward(t))
	input.machineFile = "/captured/config.yaml"
	input.machine.Run = &userconfig.RunConfig{CacheFrom: new("ci-cache")}
	input.settings.Environment = settings.Values{Options: runspec.Options{NoMemo: runspec.Setting[bool]{Optional: runspec.Optional[bool]{Set: true, Value: true}}, Output: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "plain"}}}}
	flags, args := parseRunFlags([]string{"--publish", "history", "--sw-start-at", "build/compile", "--sw-stop-at", "test/verify", "--sw-only", "build*", "--sw-no-memo=false", "--sw-workers", "2", "--sw-priority=0", "--sw-pause-before", "build,test", "--sw-pause-on-failure=false", "--", "--password", "private-value"})
	if flags.parseErr != nil {
		t.Fatal(flags.parseErr)
	}
	request, err := prepareLocalRunRequest(t.Context(), input, flags, args)
	if err != nil {
		t.Fatal(err)
	}
	options := request.Request.Options
	if !options.StartAt.Set || options.StartAt.Value != "build/compile" || options.StopAt.Value != "test/verify" || options.Only.Value != "build*" || options.StartAt.Origin.Layer != runspec.LayerFlag {
		t.Fatalf("lost sealed selections: %+v", options)
	}
	if !options.NoMemo.Set || options.NoMemo.Value || options.NoMemo.Origin.Layer != runspec.LayerFlag || options.Output.Value != "plain" || options.Output.Origin.Layer != runspec.LayerEnv || options.Workers.Value != 2 || !options.Priority.Set || options.Priority.Value != 0 || options.Publish.Value != "history" || options.CacheFrom.Value != "ci-cache" || options.CacheFrom.Origin.Layer != runspec.LayerMachine || options.CacheFrom.Origin.File != input.machineFile {
		t.Fatalf("lost C4 presence or precedence: %+v", options)
	}
	if !options.Pause.Value.OnFailure.Set || options.Pause.Value.OnFailure.Value || !reflect.DeepEqual(options.Pause.Value.Before.Value, []string{"build", "test"}) {
		t.Fatalf("lost pause controls: %+v", options.Pause)
	}
	if !request.Request.SecretArgs.Set || !reflect.DeepEqual(request.Request.SecretArgs.Value, []string{"password"}) {
		t.Fatal("lost argument classification")
	}
	flags, args = parseRunFlags([]string{"--sw-cache-from=", "--", "--password", "private-value"})
	request, err = prepareLocalRunRequest(t.Context(), input, flags, args)
	if err != nil || request.Request.Options.CacheFrom.Value != "" || request.Request.Options.CacheFrom.Origin.Layer != runspec.LayerFlag {
		t.Fatalf("explicit empty cache profile did not override machine default: %+v %v", request.Request.Options.CacheFrom, err)
	}
}

func TestLocalPreparationRiskRefusalPrecedesIntake(t *testing.T) {
	award := localTestAward(t)
	var calls []string
	deps := localRuntimeFor(t, award, true, &calls)
	resolve := deps.resolve
	deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, opts)
		runtime.prepare = func(context.Context, string, runFlags) (localRunPreparation, error) {
			input := localTestPreparation(t, award)
			input.described[0].Risks = []string{"prod"}
			return input, nil
		}
		return runtime, err
	}
	err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", "private-value"}, io.Discard, deps)
	var blocked *riskBlockedError
	if !errors.As(err, &blocked) || !reflect.DeepEqual(blocked.MissingLabels, []string{"prod"}) || !reflect.DeepEqual(calls, []string{"resolve", "close"}) {
		t.Fatalf("risk refusal=%v calls=%v", err, calls)
	}
	input := localTestPreparation(t, award)
	input.described[0].Risks = []string{"prod"}
	request, err := prepareLocalRunRequest(t.Context(), input, runFlags{allow: []string{"prod"}}, []string{"--password", "private-value"})
	if err != nil || !reflect.DeepEqual(request.Request.Options.AllowRisk.Value, []string{"prod"}) {
		t.Fatalf("risk authorization did not seal: %+v %v", request.Request.Options.AllowRisk, err)
	}
}

func TestLocalReceiptAndPrivateRunHandleSurviveCancellationAfterIntake(t *testing.T) {
	award := localTestAward(t)
	var calls []string
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deps := localRuntimeFor(t, award, false, &calls)
	resolve := deps.resolve
	deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, opts)
		controller := runtime.controller.(localTestController)
		create := controller.create
		controller.create = func(ctx context.Context, request controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			response, err := create(ctx, request)
			cancel()
			return response, err
		}
		runtime.controller = controller
		return runtime, err
	}
	path := filepath.Join(t.TempDir(), "handle.json")
	err := runLocalCommand(ctx, "ci", runFlags{runHandleFile: path}, []string{"--password", "private-value"}, io.Discard, deps)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), award.RunID) {
		t.Fatalf("cancel lost durable run identity: %v", err)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Contains(body, []byte(award.RunID)) {
		t.Fatalf("private handle=%q error=%v", body, readErr)
	}
	if strings.Contains(string(body), "private-value") || strings.Contains(string(body), "private-award") {
		t.Fatal("handle contains private execution inputs")
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("private handle mode: %v %v", info, err)
	}
	if !reflect.DeepEqual(calls, []string{"resolve", "prepare", "create", "close"}) {
		t.Fatalf("cancel reached host: %v", calls)
	}
}

func TestLocalIntakeFailureHasNoReceiptOrClaim(t *testing.T) {
	var calls []string
	deps := localRuntimeFor(t, localTestAward(t), true, &calls)
	resolve := deps.resolve
	failure := errors.New("intake refused")
	deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, opts)
		runtime.controller = localTestController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			return controllerwire.CreateRunResponse{}, failure
		}}
		return runtime, err
	}
	var out bytes.Buffer
	err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", "private-value"}, &out, deps)
	if !errors.Is(err, failure) || out.Len() != 0 || !reflect.DeepEqual(calls, []string{"resolve", "prepare", "close"}) {
		t.Fatalf("failed intake produced effects: calls=%v output=%q err=%v", calls, &out, err)
	}
	for _, format := range []string{"plain", "pretty", "json"} {
		if err := emitLocalRunReceipt(failingLocalWriter{}, controllerwire.CreateRunResponse{RunID: "r"}, format); err == nil {
			t.Fatalf("%s hid output failure", format)
		}
	}
	if err := emitLocalRunReceipt(io.Discard, controllerwire.CreateRunResponse{}, "json"); err == nil {
		t.Fatal("empty receipt reported success")
	}
}

type failingLocalWriter struct{}

func (failingLocalWriter) Write([]byte) (int, error) { return 0, errors.New("closed output") }

func TestStandaloneHostUsesFinalFlagsAndSharedHost(t *testing.T) {
	award := localTestAward(t)
	var calls []string
	deps := localRuntimeFor(t, award, false, &calls)
	resolve := deps.resolve
	db := filepath.Join(t.TempDir(), "state.db")
	env := filepath.Join(t.TempDir(), "environment.json")
	deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
		if opts.stateDB != db || opts.environmentFile != env || opts.standaloneReason != "isolated CI" {
			t.Fatalf("standalone input=%+v", opts)
		}
		return resolve(ctx, opts)
	}
	err := runLocalHostCommand(t.Context(), []string{"--run", award.RunID, "--state-db", db, "--env-file", env, "--standalone-reason", "isolated CI"}, io.Discard, deps)
	if err != nil || !reflect.DeepEqual(calls, []string{"resolve", "claim", "bind", "host", "host-close", "close"}) {
		t.Fatalf("standalone calls=%v error=%v", calls, err)
	}
	for _, args := range [][]string{{"--program", "pipeline", "run"}, {"--run", "r", "--state-db", "relative", "--standalone-reason", "CI"}, {"--run", "r", "--state-db", db}} {
		if err := runLocalHostCommand(t.Context(), args, io.Discard, localRunDependencies{resolve: func(context.Context, localRunOptions) (localRunRuntime, error) {
			t.Fatal("invalid host arguments reached native composition")
			return localRunRuntime{}, nil
		}}); err == nil {
			t.Fatalf("accepted invalid host argv %v", args)
		}
	}
}

func FuzzLocalOwnerSealCannotChange(f *testing.F) {
	f.Add("private", "foreign", int64(7))
	f.Fuzz(func(t *testing.T, value, foreign string, generation int64) {
		award := localTestAward(t)
		request := award.Request.Request
		request.RequestedArgs = map[string]string{"password": value}
		sealed, err := runspec.SealRequest(request, runspec.IntakeFacts{RunID: award.RunID})
		if err != nil {
			t.Fatal(err)
		}
		award.Request = sealed
		input, err := localRunInput(award.RunID, sealed.Hash, award)
		if err != nil {
			t.Fatal(err)
		}
		var decoded runspec.Request
		if err := json.Unmarshal(input.SealedRequest, &decoded); err != nil || !reflect.DeepEqual(decoded, sealed.Request) {
			t.Fatalf("sealed input changed: %v", err)
		}
		if foreign != decoded.RequestedArgs["password"] {
			award.Request.Request.RequestedArgs["password"] = foreign
			changedHash, err := runspec.CanonicalHash(award.Request)
			if err != nil {
				t.Fatal(err)
			}
			if changedHash != sealed.Hash {
				if _, err := localRunInput(award.RunID, sealed.Hash, award); err == nil {
					t.Fatal("changed argument retained execution authority")
				}
			}
		}
		award.Request = sealed
		award.Binding.Generation = generation
		if generation <= 0 {
			if _, err := localRunInput(award.RunID, sealed.Hash, award); err == nil {
				t.Fatal("unfenced owner was accepted")
			}
		}
	})
}

func FuzzLocalRunPrivateResponseDiagnostics(f *testing.F) {
	f.Add("secret", uint8(0))
	f.Add("private-response", uint8(1))
	f.Add("private-response", uint8(2))
	f.Fuzz(func(t *testing.T, value string, stage uint8) {
		secret := "private-token-" + hex.EncodeToString([]byte(value))
		award := localTestAward(t)
		var calls []string
		deps := localRuntimeFor(t, award, true, &calls)
		resolve := deps.resolve
		deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
			runtime, err := resolve(ctx, opts)
			if stage%3 == 0 {
				runtime.prepare = func(context.Context, string, runFlags) (localRunPreparation, error) {
					return localRunPreparation{}, errors.New("preparation failed: " + secret)
				}
				return runtime, err
			}
			runtime.controller = localTestController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
				response := controllerwire.CreateRunResponse{HereBearer: secret, Request: runspec.SealedRequest{Request: runspec.Request{RequestedArgs: map[string]string{"token": secret}}}}
				if stage%3 == 1 {
					return response, errors.New("invalid response: " + secret)
				}
				return response, nil
			}}
			return runtime, err
		}
		var out bytes.Buffer
		err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", secret}, &out, deps)
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
			t.Fatalf("private response escaped through error or receipt: %v %q", err, &out)
		}
	})
}

func FuzzDetachedIntakeDiagnosticsNeverExposeArguments(f *testing.F) {
	f.Add([]byte("value"), false)
	f.Add([]byte("value"), true)
	f.Fuzz(func(t *testing.T, data []byte, canonical bool) {
		secret := "private-argument-" + hex.EncodeToString(data)
		award := localTestAward(t)
		var calls []string
		deps := localRuntimeFor(t, award, true, &calls)
		resolve := deps.resolve
		deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
			runtime, err := resolve(ctx, options)
			runtime.controller = localTestController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
				cause := error(errors.New("intake failed: " + secret))
				if canonical {
					cause = &apierr.Error{Code: apierr.InvalidArgs, Message: "intake failed: " + secret}
				}
				return controllerwire.CreateRunResponse{}, cause
			}}
			return runtime, err
		}
		var out bytes.Buffer
		err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", secret}, &out, deps)
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
			t.Fatal("intake diagnostic exposed an argument")
		}
		var structured *apierr.Error
		if errors.As(err, &structured) {
			raw, marshalErr := json.Marshal(structured)
			if marshalErr != nil || strings.Contains(string(raw), secret) {
				t.Fatal("structured intake diagnostic exposed an argument")
			}
		}
	})
}

func FuzzLocalIntakeDiagnosticsNeverExposeRequesterSecrets(f *testing.F) {
	f.Add([]byte("requester credential"), false)
	f.Add([]byte{0, 255, 34}, true)
	f.Fuzz(func(t *testing.T, data []byte, canonical bool) {
		if len(data) > 256 {
			return
		}
		secret := "requester-secret-" + hex.EncodeToString(data)
		var calls []string
		deps := localRuntimeFor(t, localTestAward(t), true, &calls)
		resolve := deps.resolve
		deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
			runtime, err := resolve(ctx, options)
			prepare := runtime.prepare
			runtime.prepare = func(ctx context.Context, pipeline string, flags runFlags) (localRunPreparation, error) {
				input, err := prepare(ctx, pipeline, flags)
				input.localSecrets = map[string]string{"TOKEN": secret}
				return input, err
			}
			runtime.controller = localTestController{create: func(_ context.Context, request controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
				if request.LocalSecrets["TOKEN"] != secret || len(request.Request.LocalSecretNames) != 1 {
					t.Fatal("intake lost its private requester secret")
				}
				var failure error = errors.New("intake failed: " + secret)
				if canonical {
					failure = &apierr.Error{Code: apierr.InvalidArgs, Message: "intake failed: " + secret}
				}
				return controllerwire.CreateRunResponse{}, failure
			}}
			return runtime, err
		}
		var output bytes.Buffer
		err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, nil, &output, deps)
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(output.String(), secret) {
			t.Fatal("requester secret escaped through intake diagnostics")
		}
		var structured *apierr.Error
		if errors.As(err, &structured) {
			body, err := json.Marshal(structured)
			if err != nil || bytes.Contains(body, []byte(secret)) {
				t.Fatal("structured intake diagnostic exposed requester secret")
			}
		}
	})
}

func TestLocalOwnerResumePreservesAcceptedPlan(t *testing.T) {
	award := localTestAward(t)
	plan := json.RawMessage(`{"jobs":[]}`)
	spec, err := runspec.SealSpec(runspec.SealInput{Request: award.Request, Plan: plan, Now: time.Unix(2, 0), Resolved: runspec.Resolved{SecretArgs: []string{"password"}, Args: map[string]string{"password": "private-value"}}})
	if err != nil {
		t.Fatal(err)
	}
	award.Accepted = &controllerwire.PlanAccepted{Spec: spec, Plan: plan, PlanHash: spec.PlanHash}
	input, err := localRunInput(award.RunID, award.Request.Hash, award)
	if err != nil || input.Accepted == nil {
		t.Fatalf("resume dropped accepted plan: %+v %v", input, err)
	}
	var got runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &got); err != nil || !reflect.DeepEqual(got, spec) || input.Accepted.PlanHash != string(spec.PlanHash) || input.Accepted.SpecHash != string(spec.Hash) {
		t.Fatalf("resume changed accepted state: %+v %v", input.Accepted, err)
	}
	award.Accepted.Plan = json.RawMessage(`{"jobs":[{}]}`)
	if _, err := localRunInput(award.RunID, award.Request.Hash, award); err == nil {
		t.Fatal("resume accepted changed plan bytes")
	}
}

type localReadOnlyCache struct{ blob.RemoteCache }

func TestLocalRemoteCacheRequiresTheSealedOptIn(t *testing.T) {
	for _, profile := range []string{"", "ci-cache"} {
		t.Run(profile, func(t *testing.T) {
			var calls []string
			deps := localRuntimeFor(t, localTestAward(t), false, &calls)
			resolve := deps.resolve
			cache := &localReadOnlyCache{}
			deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
				runtime, err := resolve(ctx, opts)
				runtime.remoteCache = cache
				bind := runtime.bind
				runtime.bind = func(ctx context.Context, award controllerwire.ClaimedUnit) (hereHostSession, error) {
					session, err := bind(ctx, award)
					host := session.host
					session.host = hereTestHost(func(ctx context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
						if profile == "" && input.Storage.RemoteCache != nil || profile != "" && input.Storage.RemoteCache != cache {
							t.Fatal("cache binding ignored sealed opt-in")
						}
						return host.Run(ctx, input)
					})
					return session, err
				}
				return runtime, err
			}
			if err := runLocalCommand(t.Context(), "ci", runFlags{cacheFrom: profile}, []string{"--password", "private-value"}, io.Discard, deps); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, flags := range []runFlags{{cacheFrom: "cache"}} {
		var calls []string
		deps := localRuntimeFor(t, localTestAward(t), false, &calls)
		err := runLocalCommand(t.Context(), "ci", flags, []string{"--password", "private-value"}, io.Discard, deps)
		var unavailable *apierr.Error
		if !errors.As(err, &unavailable) || unavailable.Code != apierr.Unavailable || !reflect.DeepEqual(calls, []string{"resolve", "prepare", "close"}) {
			t.Fatalf("unbound storage reached intake: calls=%v error=%v", calls, err)
		}
	}
}

func TestLocalPublicationFailurePreservesExecutionVerdict(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var calls []string
		deps := localRuntimeFor(t, localTestAward(t), false, &calls)
		resolve := deps.resolve
		published := false
		deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
			runtime, err := resolve(ctx, opts)
			runtime.publish = func(_ context.Context, profile, run string) error {
				published = true
				if profile != "history" || run != "local-run" {
					t.Fatal("publication lost its profile or local run")
				}
				if fail {
					return errors.New("profile unavailable")
				}
				return nil
			}
			return runtime, err
		}
		if err := runLocalCommand(t.Context(), "ci", runFlags{publish: "history"}, []string{"--password", "private-value"}, io.Discard, deps); err != nil || !published {
			t.Fatalf("publication changed local verdict: published=%v err=%v", published, err)
		}
	}
}

func TestLocalPublicationHasDeadlineAndSecondInterruptAbandonsIt(t *testing.T) {
	var calls []string
	deps := localRuntimeFor(t, localTestAward(t), false, &calls)
	resolve := deps.resolve
	signals := make(chan os.Signal, 2)
	deps.interrupts = signals
	stderr, err := os.CreateTemp(t.TempDir(), "publication-stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	previous := os.Stderr
	os.Stderr = stderr
	defer func() { os.Stderr = previous }()
	deps.resolve = func(ctx context.Context, opts localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, opts)
		runtime.publish = func(ctx context.Context, _, _ string) error {
			_, bounded := ctx.Deadline()
			if !bounded {
				t.Error("publication has no bounded request deadline")
				return errors.New("unbounded publication")
			}
			signals <- os.Interrupt
			signals <- os.Interrupt
			<-ctx.Done()
			return ctx.Err()
		}
		return runtime, err
	}
	if err := runLocalCommand(t.Context(), "ci", runFlags{publish: "history"}, []string{"--password", "private-value"}, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(stderr.Name())
	if err != nil || !strings.Contains(string(body), "stayed local-only") {
		t.Fatalf("abandoned publication notice: %q %v", body, err)
	}
}

func TestLocalAttachedRunStreamsCommittedLogs(t *testing.T) {
	var calls []string
	deps := localRuntimeFor(t, localTestAward(t), false, &calls)
	resolve := deps.resolve
	followed := false
	deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, options)
		runtime.follow = func(_ context.Context, run, format string, out io.Writer) error {
			followed = true
			if run != "local-run" || format != "json" {
				return errors.New("log attachment lost run or output selection")
			}
			_, err := fmt.Fprintln(out, `{"message":"masked output ***"}`)
			return err
		}
		return runtime, err
	}
	var out bytes.Buffer
	flags, args := parseRunFlags([]string{"-o=json", "--", "--password", "private-value"})
	if err := runLocalCommand(t.Context(), "ci", flags, args, &out, deps); err != nil {
		t.Fatal(err)
	}
	if !followed || !strings.Contains(out.String(), "masked output ***") {
		t.Fatalf("attached run lost logs: %s", &out)
	}
}
