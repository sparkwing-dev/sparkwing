package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/quick"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type hereTestController struct {
	controllerwire.Controller
	read  func(context.Context, string) (controllerwire.RunDetail, error)
	claim func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
}

func (c hereTestController) ReadRun(ctx context.Context, id string) (controllerwire.RunDetail, error) {
	return c.read(ctx, id)
}

func (c hereTestController) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return c.claim(ctx, request)
}

type hereTestHost func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error)

func (h hereTestHost) Run(ctx context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
	return h(ctx, input)
}

func hereAward(t *testing.T) controllerwire.ClaimedUnit {
	t.Helper()
	runner := runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	request, err := runspec.SealRequest(runspec.Request{
		Version: 1, Team: "team", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost,
		Origin: runspec.OriginTrigger, Options: runspec.Options{Runner: runner},
		Git:           runspec.Git{Repository: runspec.Repository{Key: "gh:42"}, SHA: "0123456789abcdef0123456789abcdef01234567"},
		Source:        runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567"},
		RequestedArgs: map[string]string{"password": "secret-argument"},
	}, runspec.IntakeFacts{RunID: "original-run", AdmittedAt: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return controllerwire.ClaimedUnit{
		Unit: capability.UnitTrigger, Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect,
		RunID: "retry-run", UnitID: "owner-unit", Capability: "awarded-private-capability", Request: request,
		Binding:        capability.Binding{Kind: capability.BindingTrigger, RunID: "retry-run", Generation: 7, Holder: "granted-holder"},
		LeaseExpiresAt: time.Unix(900, 0),
		Runner:         nodeproto.RunnerIdentity{Name: "granted-here", Type: "here", Labels: []string{"os=linux"}},
	}
}

func TestHereResumeUsesOnlyAwardedInputs(t *testing.T) {
	award := hereAward(t)
	plan := json.RawMessage(`{"jobs":[]}`)
	spec, err := runspec.SealSpec(runspec.SealInput{
		Request: award.Request, Plan: plan, Now: time.Unix(2, 0),
		Resolved: runspec.Resolved{SecretArgs: []string{"password"}, Args: map[string]string{"password": "secret-argument"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	award.Accepted = &controllerwire.PlanAccepted{Spec: spec, Plan: plan, PlanHash: spec.PlanHash}
	store := hereCredentials{dir: t.TempDir()}
	if err := store.save("https://controller.example", "retry-run", "retained-private-bearer"); err != nil {
		t.Fatal(err)
	}
	var calls []string
	closed := 0
	deps := hereDependencies{
		credentials: func() (hereCredentials, error) { return store, nil }, version: "v1.2.3",
		resolve: func(ctx context.Context, options client.EngineOptions) (hereRuntime, error) {
			bearer, err := options.Bearer(ctx)
			if err != nil || bearer != "retained-private-bearer" || options.URL != "https://controller.example" {
				t.Fatal("resume lost its private connection binding")
			}
			return hereRuntime{
				controller: hereTestController{claim: func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					calls = append(calls, "claim")
					if request.RunID != "retry-run" || request.JobID != "" || request.RunnerVersion != "v1.2.3" || !reflect.DeepEqual(request.Kinds, []capability.UnitKind{capability.UnitTrigger}) || len(request.Labels) != 0 {
						t.Fatalf("wrong named claim: %+v", request)
					}
					return controllerwire.ClaimResponse{Claim: &award}, nil
				}},
				bind: func(ctx context.Context, options client.EngineOptions, got controllerwire.ClaimedUnit) (hereHostSession, error) {
					calls = append(calls, "bind")
					token, err := options.Bearer(ctx)
					if err != nil || token != award.Capability || !reflect.DeepEqual(got, award) {
						t.Fatal("binder lost server-granted identity or authority")
					}
					return hereHostSession{close: func() error { closed++; return nil }, host: hereTestHost(func(ctx context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
						calls = append(calls, "host")
						var request runspec.Request
						var accepted runspec.Spec
						if err := json.Unmarshal(input.SealedRequest, &request); err != nil {
							t.Fatal(err)
						}
						if input.Accepted == nil {
							t.Fatal("resume discarded the accepted plan")
						}
						if err := json.Unmarshal(input.Accepted.Spec, &accepted); err != nil {
							t.Fatal(err)
						}
						if input.RunID != "retry-run" || input.RequestHash != string(award.Request.Hash) || !reflect.DeepEqual(request, award.Request.Request) || !reflect.DeepEqual(accepted, spec) || input.Accepted.RunID != "retry-run" || input.Accepted.PlanHash != string(spec.PlanHash) {
							t.Fatal("resume changed the request or accepted seal")
						}
						return nodehost.RunCompletion{Outcome: "success"}, nil
					})}, nil
				},
			}, nil
		},
	}
	completion, err := resumeHereRun(t.Context(), client.EngineOptions{URL: "https://controller.example"}, "retry-run", deps)
	if err != nil || completion.Outcome != "success" || closed != 1 || !reflect.DeepEqual(calls, []string{"claim", "bind", "host"}) {
		t.Fatalf("completion=%+v calls=%v closed=%d error=%v", completion, calls, closed, err)
	}
}

func TestHereHostPreservesAwardedRunnerProperty(t *testing.T) {
	base := hereAward(t)
	property := func(name, kind string, labels []string, ambient nodeproto.RunnerIdentity) bool {
		award := base
		award.Runner = nodeproto.RunnerIdentity{Name: name, Type: kind, Labels: labels}
		called, closed := false, false
		completion, err := runHereAward(t.Context(), client.EngineOptions{}, award.RunID, award,
			func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error) {
				return hereHostSession{
					input: nodehost.RunInput{GrantedRunner: ambient}, close: func() error { closed = true; return nil },
					host: hereTestHost(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
						called = true
						if !reflect.DeepEqual(input.GrantedRunner, award.Runner) {
							return nodehost.RunCompletion{}, errors.New("host received ambient runner identity")
						}
						return nodehost.RunCompletion{Outcome: "success"}, nil
					}),
				}, nil
			})
		return err == nil && called && closed && completion.Outcome == "success"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestHereCredentialsArePrivateAndBound(t *testing.T) {
	store := hereCredentials{dir: t.TempDir()}
	if err := store.save("https://one.example", "run", "private-bearer"); err != nil {
		t.Fatal(err)
	}
	if err := store.save("https://one.example/", "run", "private-bearer"); err != nil {
		t.Fatal(err)
	}
	for _, selection := range [][2]string{{"https://two.example", "run"}, {"https://one.example", "other-run"}} {
		if _, err := store.load(selection[0], selection[1]); err == nil {
			t.Fatal("foreign binding loaded a bearer")
		}
	}
	if err := store.save("https://one.example", "run", "replacement-bearer"); err == nil {
		t.Fatal("retained authority was replaced")
	}
	path, err := store.path("https://one.example", "run")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("private file: %v %v", info, err)
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("private directory: %v %v", info, err)
	}
	f, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHereReplayWithoutRetainedBearerNeverClaims(t *testing.T) {
	deps := hereDependencies{
		credentials: func() (hereCredentials, error) { return hereCredentials{dir: t.TempDir()}, nil },
		resolve: func(context.Context, client.EngineOptions) (hereRuntime, error) {
			t.Fatal("lost handoff claimed a run")
			return hereRuntime{}, nil
		},
	}
	err := handoffHereRun(t.Context(), client.EngineOptions{URL: "https://controller.example"}, controllerwire.CreateRunResponse{RunID: "r", Replayed: true}, deps)
	if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "fresh trigger request") {
		t.Fatalf("lost handoff: %v", err)
	}
}

func TestHereReplayCannotReplaceOneTimeAuthority(t *testing.T) {
	deps := hereDependencies{credentials: func() (hereCredentials, error) {
		t.Fatal("replayed one-time authority reached private storage")
		return hereCredentials{}, nil
	}}
	err := handoffHereRun(t.Context(), client.EngineOptions{URL: "https://controller.example"}, controllerwire.CreateRunResponse{RunID: "run", Replayed: true, HereBearer: "replacement-bearer"}, deps)
	var failure *apierr.Error
	if exitCodeFor(err) != 3 || !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
		t.Fatalf("invalid replay: %v", err)
	}
}

func TestHereResumeCommandResolvesNumberWithoutNewIntake(t *testing.T) {
	var out bytes.Buffer
	options := client.EngineOptions{URL: "https://controller.example"}
	read := hereTestController{read: func(_ context.Context, id string) (controllerwire.RunDetail, error) {
		if id != "412" {
			t.Fatalf("selector = %q", id)
		}
		return controllerwire.RunDetail{ID: "stable-run"}, nil
	}}
	called := false
	resume := func(_ context.Context, got client.EngineOptions, id string) (nodehost.RunCompletion, error) {
		called = true
		if got.URL != options.URL || id != "stable-run" {
			t.Fatal("resume binding changed")
		}
		return nodehost.RunCompletion{Outcome: "success"}, nil
	}
	err := runHereResumeCommand(t.Context(), triggerFlags{resume: "412", profile: "prod", output: "json"}, &out,
		func(context.Context, string) (client.EngineOptions, func(), error) { return options, func() {}, nil },
		func(client.EngineOptions) (controllerwire.Controller, error) { return read, nil }, resume)
	if err != nil || !called || out.String() != "{\"run_id\":\"stable-run\",\"outcome\":\"success\"}\n" {
		t.Fatalf("output=%q error=%v", out.String(), err)
	}
}

func FuzzHereCredentialsNeverCrossBindings(f *testing.F) {
	f.Add("private-bearer", "../../run", "other-run")
	f.Fuzz(func(t *testing.T, bearer, run, other string) {
		if bearer == "" || run == "" || other == "" || run == other || !utf8.ValidString(bearer) || !utf8.ValidString(run) || !utf8.ValidString(other) {
			t.Skip()
		}
		store := hereCredentials{dir: t.TempDir()}
		if err := store.save("https://one.example", run, bearer); err != nil {
			t.Fatal(err)
		}
		for _, pair := range [][2]string{{"https://two.example", run}, {"https://one.example", other}} {
			if _, err := store.load(pair[0], pair[1]); err == nil {
				t.Fatal("cross-binding credential read")
			}
		}
		got, err := store.load("https://one.example/", run)
		if err != nil || got != bearer {
			t.Fatal("own binding did not round-trip")
		}
		path, err := store.path("https://one.example", run)
		if err != nil || filepath.Dir(path) != filepath.Join(store.dir, "here") {
			t.Fatal("selector escaped the credential directory")
		}
	})
}

func TestHereRejectsWrongAwardsBeforeHostConstruction(t *testing.T) {
	for name, mutate := range map[string]func(*controllerwire.ClaimedUnit){
		"foreign run":   func(a *controllerwire.ClaimedUnit) { a.RunID = "foreign-run" },
		"foreign fence": func(a *controllerwire.ClaimedUnit) { a.Binding.RunID = "foreign-run" },
		"job":           func(a *controllerwire.ClaimedUnit) { a.JobID = "job" },
		"unit":          func(a *controllerwire.ClaimedUnit) { a.Unit = capability.UnitPlan },
		"purpose":       func(a *controllerwire.ClaimedUnit) { a.Purpose = capability.PurposeHere },
		"bootstrap":     func(a *controllerwire.ClaimedUnit) { a.Delivery = capability.DeliveryBootstrap },
		"holder":        func(a *controllerwire.ClaimedUnit) { a.Binding.Holder = "" },
		"generation":    func(a *controllerwire.ClaimedUnit) { a.Binding.Generation = 0 },
		"missing seal":  func(a *controllerwire.ClaimedUnit) { a.Request.Hash = "" },
		"changed seal":  func(a *controllerwire.ClaimedUnit) { a.Request.Request.Pipeline = "different" },
		"non-here":      func(a *controllerwire.ClaimedUnit) { a.Request.Request.Options.Runner.Value.Kind = runspec.RunnerAny },
	} {
		t.Run(name, func(t *testing.T) {
			award := hereAward(t)
			mutate(&award)
			if _, err := hereRunInput("retry-run", award); err == nil {
				t.Fatal("invalid award accepted")
			}
		})
	}
}

func TestHereHandoffRetainsAuthorityBeforeNativeRefusal(t *testing.T) {
	store := hereCredentials{dir: t.TempDir()}
	refusal := &apierr.Error{Code: apierr.Unavailable, Message: "run host binding unavailable"}
	deps := hereDependencies{
		credentials: func() (hereCredentials, error) { return store, nil },
		resolve:     func(context.Context, client.EngineOptions) (hereRuntime, error) { return hereRuntime{}, refusal },
	}
	err := handoffHereRun(t.Context(), client.EngineOptions{URL: "https://controller.example"}, controllerwire.CreateRunResponse{RunID: "run", HereBearer: "private-bearer"}, deps)
	if exitCodeFor(err) != 3 || !errors.Is(err, refusal) {
		t.Fatalf("native refusal lost: %v", err)
	}
	bearer, err := store.load("https://controller.example", "run")
	if err != nil || bearer != "private-bearer" {
		t.Fatal("native refusal lost the one-time bearer")
	}
}

func TestHereCredentialsRefuseTamperingAndMalformedFiles(t *testing.T) {
	for _, body := range []string{
		`{"controller":"https://other.example","run_id":"run","bearer":"private-secret"}`,
		`{"controller":"https://one.example","run_id":"other","bearer":"private-secret"}`,
		`{"controller":"https://one.example","run_id":"run","bearer":""}`,
		`{"controller":"https://one.example","run_id":"run","bearer":"private-secret","unknown":true}`,
		`{"bearer":"private-secret"} {}`,
		`{"bearer":"private-secret","bad":`,
	} {
		store := hereCredentials{dir: t.TempDir()}
		if err := store.save("https://one.example", "run", "private-secret"); err != nil {
			t.Fatal(err)
		}
		path, err := store.path("https://one.example", "run")
		if err != nil {
			t.Fatal(err)
		}
		if err := fssecure.WriteFile(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.load("https://one.example", "run"); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("malformed credential was accepted or disclosed")
		}
	}
}

func TestHereCredentialsRefuseWritableTargetsAndEscapingSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode and symlink check")
	}
	store := hereCredentials{dir: t.TempDir()}
	if err := store.save("https://one.example", "run", "private-secret"); err != nil {
		t.Fatal(err)
	}
	path, err := store.path("https://one.example", "run")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load("https://one.example", "run"); err == nil {
		t.Fatal("public credential file accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(store.dir, "target")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load("https://one.example", "run"); err == nil {
		t.Fatal("symlinked credential file accepted")
	}
	if err := store.save("https://one.example", "run", "replacement-secret"); err == nil {
		t.Fatal("symlinked credential was replaced")
	}
	body, err := os.ReadFile(target)
	if err != nil || strings.Contains(string(body), "replacement-secret") || !strings.Contains(string(body), "private-secret") {
		t.Fatal("symlink target changed")
	}
}

func TestHereCancellationDoesNotStartHosting(t *testing.T) {
	for _, point := range []string{"before-load", "after-resolve", "after-claim", "after-bind"} {
		t.Run(point, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := hereCredentials{dir: t.TempDir()}
			if err := store.save("https://controller.example", "retry-run", "private-bearer"); err != nil {
				t.Fatal(err)
			}
			if point == "before-load" {
				cancel()
			}
			closed := false
			award := hereAward(t)
			deps := hereDependencies{
				credentials: func() (hereCredentials, error) {
					if point == "before-load" {
						t.Fatal("cancelled resume loaded credentials")
					}
					return store, nil
				},
				resolve: func(context.Context, client.EngineOptions) (hereRuntime, error) {
					if point == "after-resolve" {
						cancel()
					}
					return hereRuntime{
						controller: hereTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
							if point == "after-resolve" {
								t.Fatal("cancelled resume claimed")
							}
							if point == "after-claim" {
								cancel()
							}
							return controllerwire.ClaimResponse{Claim: &award}, nil
						}},
						bind: func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error) {
							if point == "after-claim" {
								t.Fatal("cancelled resume constructed a host")
							}
							cancel()
							return hereHostSession{close: func() error { closed = true; return nil }, host: hereTestHost(func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error) {
								t.Fatal("cancelled resume started a host")
								return nodehost.RunCompletion{}, nil
							})}, nil
						},
					}, nil
				},
			}
			_, err := resumeHereRun(ctx, client.EngineOptions{URL: "https://controller.example"}, "retry-run", deps)
			if !errors.Is(err, context.Canceled) || exitCodeFor(err) != 3 || point == "after-bind" && !closed {
				t.Fatalf("closed=%v error=%v", closed, err)
			}
		})
	}
}

func FuzzHereDiagnosticsExcludePrivateInputs(f *testing.F) {
	f.Add("private-credential", "secret-argument")
	f.Fuzz(func(t *testing.T, secret, argument string) {
		if !utf8.ValidString(secret) || !utf8.ValidString(argument) {
			t.Skip()
		}
		secret = "here-private-bearer:" + secret
		argument = "here-private-argument:" + argument
		store := hereCredentials{dir: t.TempDir()}
		if err := store.save("https://controller.example", "retry-run", secret); err != nil {
			t.Fatal(err)
		}
		award := hereAward(t)
		award.Capability = secret
		award.Request.Request.RequestedArgs["password"] = argument
		award.Request.Hash, _ = runspec.CanonicalHash(award.Request)
		refusal := &apierr.Error{Code: apierr.Unavailable, Message: secret, Hint: argument, Details: map[string]any{"secret": secret, "argument": argument}}
		wantCode := apierr.Unavailable
		if len(secret)%2 == 0 {
			refusal.Code = apierr.Code(secret)
			wantCode = apierr.Internal
		}
		deps := hereDependencies{
			credentials: func() (hereCredentials, error) { return store, nil },
			resolve: func(context.Context, client.EngineOptions) (hereRuntime, error) {
				return hereRuntime{
					controller: hereTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						return controllerwire.ClaimResponse{Claim: &award}, nil
					}},
					bind: func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error) {
						return hereHostSession{}, refusal
					},
				}, nil
			},
		}
		_, err := resumeHereRun(t.Context(), client.EngineOptions{URL: "https://controller.example"}, "retry-run", deps)
		var structured *apierr.Error
		if !errors.Is(err, refusal) || !errors.As(err, &structured) {
			t.Fatal("masked error lost its cause or code")
		}
		body, marshalErr := json.Marshal(structured)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if structured.Message == secret || structured.Hint == argument || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), argument) || structured.Details["secret"] == secret || structured.Details["argument"] == argument {
			t.Fatal("private diagnostic escaped masking")
		}
		if len(body) == 0 || structured.Code != wantCode {
			t.Fatal("masked diagnostic is invalid")
		}
	})
}

func TestHereHostCleanupFailureIsIncomplete(t *testing.T) {
	store := hereCredentials{dir: t.TempDir()}
	if err := store.save("https://controller.example", "retry-run", "private-bearer"); err != nil {
		t.Fatal(err)
	}
	award := hereAward(t)
	deps := hereDependencies{
		credentials: func() (hereCredentials, error) { return store, nil },
		resolve: func(context.Context, client.EngineOptions) (hereRuntime, error) {
			return hereRuntime{
				controller: hereTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					return controllerwire.ClaimResponse{Claim: &award}, nil
				}},
				bind: func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error) {
					return hereHostSession{close: func() error { return io.ErrClosedPipe }, host: hereTestHost(func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error) {
						return nodehost.RunCompletion{Outcome: "success"}, nil
					})}, nil
				},
			}, nil
		},
	}
	_, err := resumeHereRun(t.Context(), client.EngineOptions{URL: "https://controller.example"}, "retry-run", deps)
	if !errors.Is(err, io.ErrClosedPipe) || exitCodeFor(err) != 3 {
		t.Fatalf("cleanup failure: %v", err)
	}
}

func TestHereHostAlwaysJoinsReturnedSessionProperty(t *testing.T) {
	award := hereAward(t)
	bindingFailure, hostFailure, cleanupFailure := errors.New("binding failed"), errors.New("host failed"), errors.New("cleanup failed")
	property := func(bindFails, hostFails, closeFails, cancelBinding, missingHost bool) bool {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		closed, hosted := 0, false
		completion, err := runHereAward(ctx, client.EngineOptions{}, award.RunID, award,
			func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error) {
				session := hereHostSession{close: func() error {
					closed++
					if closeFails {
						return cleanupFailure
					}
					return nil
				}}
				if !missingHost {
					session.host = hereTestHost(func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error) {
						hosted = true
						if hostFails {
							return nodehost.RunCompletion{}, hostFailure
						}
						return nodehost.RunCompletion{Outcome: "success"}, nil
					})
				}
				if cancelBinding {
					cancel()
				}
				if bindFails {
					return session, bindingFailure
				}
				return session, nil
			})
		shouldHost := !bindFails && !cancelBinding && !missingHost
		if closed != 1 || hosted != shouldHost || errors.Is(err, cleanupFailure) != closeFails || errors.Is(err, bindingFailure) != bindFails || errors.Is(err, hostFailure) != (shouldHost && hostFails) {
			return false
		}
		if bindFails || hostFails || closeFails || cancelBinding || missingHost {
			return exitCodeFor(err) == 3
		}
		return err == nil && completion.Outcome == "success"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func FuzzHereAwardsCannotChangeRunBinding(f *testing.F) {
	f.Add("retry-run", "retry-run", "granted-holder", int64(7))
	f.Add("foreign-run", "retry-run", "granted-holder", int64(7))
	f.Add("retry-run", "foreign-run", "granted-holder", int64(7))
	f.Fuzz(func(t *testing.T, unitRun, bindingRun, holder string, generation int64) {
		award := hereAward(t)
		award.RunID, award.Binding.RunID, award.Binding.Holder, award.Binding.Generation = unitRun, bindingRun, holder, generation
		input, err := hereRunInput("retry-run", award)
		valid := unitRun == "retry-run" && bindingRun == "retry-run" && holder != "" && generation > 0
		if valid && (err != nil || input.RunID != "retry-run") || !valid && err == nil {
			t.Fatal("here award crossed its named run fence")
		}
	})
}

func FuzzHerePreparationCannotReplaceAward(f *testing.F) {
	f.Add("forged-run", "forged-holder", "forged-label", int64(51))
	f.Add("", "", "", int64(0))
	f.Fuzz(func(t *testing.T, run, holder, label string, generation int64) {
		award := hereAward(t)
		expected, err := nodehost.NewRunInput(award)
		if err != nil {
			t.Fatal(err)
		}
		called, closed := false, false
		_, err = runHereAward(t.Context(), client.EngineOptions{}, award.RunID, award,
			func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error) {
				prepared := nodehost.RunInput{
					RunID: run, RequestHash: run, SealedRequest: []byte(run),
					GrantedRunner: nodeproto.RunnerIdentity{Name: run, Type: holder, Labels: []string{label}},
					Owner:         store.Actor{Kind: store.ActorOperator, CapabilityPrefix: "bound-authority", Owner: store.OwnerLeaseRef{RunID: run, Holder: holder, Generation: generation}},
				}
				award.Runner.Labels[0] = label
				return hereHostSession{input: prepared, close: func() error { closed = true; return nil }, host: hereTestHost(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
					called = true
					if input.RunID != expected.RunID || input.RequestHash != expected.RequestHash || !bytes.Equal(input.SealedRequest, expected.SealedRequest) || input.Owner.Kind != expected.Owner.Kind || input.Owner.Owner != expected.Owner.Owner || !reflect.DeepEqual(input.GrantedRunner, expected.GrantedRunner) || input.Owner.CapabilityPrefix != "bound-authority" {
						t.Fatal("preparation replaced awarded identity or sealed input")
					}
					var request runspec.Request
					if json.Unmarshal(input.SealedRequest, &request) != nil || !reflect.DeepEqual(request, award.Request.Request) {
						t.Fatal("host did not receive the request shape consumed by OwnerHost")
					}
					return nodehost.RunCompletion{Outcome: "success"}, nil
				})}, nil
			})
		if err != nil || !called || !closed {
			t.Fatalf("hosted=%t closed=%t error=%v", called, closed, err)
		}
	})
}

func TestHereAdmissionCallbackRetainsAuthorityBeforePublishingHandle(t *testing.T) {
	credentials := hereCredentials{dir: t.TempDir()}
	options := client.EngineOptions{URL: "https://cloud.example"}
	called := false
	failure := errors.New("handle write failed")
	err := handoffHereRun(t.Context(), options, controllerwire.CreateRunResponse{RunID: "burst", HereBearer: "private"}, hereDependencies{
		credentials: func() (hereCredentials, error) { return credentials, nil },
		admitted: func(response controllerwire.CreateRunResponse) error {
			called = true
			value, err := credentials.load(options.URL, response.RunID)
			if err != nil || value != "private" {
				t.Fatalf("authority missing at publication: %q %v", value, err)
			}
			return failure
		},
		resolve: func(context.Context, client.EngineOptions) (hereRuntime, error) {
			t.Fatal("hosted after failed publication")
			return hereRuntime{}, nil
		},
	})
	if !called || !errors.Is(err, failure) {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

func FuzzHereHostRequiresExecutionAward(f *testing.F) {
	f.Add("owner", uint16(7))
	f.Fuzz(func(t *testing.T, holder string, generation uint16) {
		if holder == "" || len(holder) > 128 {
			t.Skip()
		}
		award := hereAward(t)
		award.Binding.Holder, award.Binding.Generation = holder, int64(generation)+1
		if _, err := hereRunInput(award.RunID, award); err != nil {
			t.Fatal(err)
		}
		award.Purpose = capability.PurposeHere
		if _, err := hereRunInput(award.RunID, award); err == nil {
			t.Fatal("reservation accepted as an execution award")
		}
		award.Purpose = capability.PurposeTrigger
		award.Binding.RunID += "-foreign"
		if _, err := hereRunInput(award.RunID, award); err == nil {
			t.Fatal("foreign execution fence accepted")
		}
	})
}
