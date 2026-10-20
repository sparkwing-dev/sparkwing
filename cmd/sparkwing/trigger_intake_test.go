package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type initialTriggerController struct {
	controllerwire.Controller
	create func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error)
	read   func(context.Context, string) (controllerwire.RunDetail, error)
}

func (c initialTriggerController) CreateRun(ctx context.Context, request controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
	return c.create(ctx, request)
}

func (c initialTriggerController) ReadRun(ctx context.Context, run string) (controllerwire.RunDetail, error) {
	return c.read(ctx, run)
}

func initialTriggerRequest(here bool) controllerwire.CreateRunRequest {
	request := controllerwire.CreateRunRequest{Request: runspec.Request{
		Version: 1, Team: "team", Pipeline: "deploy-prod", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
		RequestedArgs: map[string]string{"rollout.image": "release"},
		Git:           runspec.Git{Repository: runspec.Repository{Key: "gh:17"}, Ref: "main", SHA: "0123456789abcdef0123456789abcdef01234567"},
		Source:        runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567", Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app.git", PipelineDir: ".sparkwing"}},
	}}
	if here {
		request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	}
	return request
}

func initialTriggerResponse(t testing.TB, request controllerwire.CreateRunRequest) controllerwire.CreateRunResponse {
	t.Helper()
	request.Request.Dispatch = store.SelectDispatch(request.Request)
	seal, err := runspec.SealRequest(request.Request, runspec.IntakeFacts{RunID: "initial-run", AdmittedAt: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return controllerwire.CreateRunResponse{RunID: seal.RunID, Number: 17, Request: seal}
}

func TestInitialTriggerDetachedUsesFinalIntakeAndSafeReceipt(t *testing.T) {
	request := initialTriggerRequest(false)
	request.IdempotencyKey = "selected-intake"
	response := initialTriggerResponse(t, request)
	response.Replayed = true
	var got controllerwire.CreateRunRequest
	var out bytes.Buffer
	err := submitTriggeredRun(t.Context(), request, true, "json", &out, triggerSubmission{
		Controller: initialTriggerController{create: func(_ context.Context, input controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			got = input
			return response, nil
		}},
		follow: func(context.Context, string) (string, error) { t.Fatal("detached trigger followed"); return "", nil },
	})
	if err != nil || !reflect.DeepEqual(got, request) || out.String() != "{\"run_id\":\"initial-run\",\"number\":17,\"replayed\":true}\n" {
		t.Fatalf("submission=%+v output=%s error=%v", got, &out, err)
	}
}

func TestInitialTriggerHereKeepsCompletePrivateHandoff(t *testing.T) {
	request := initialTriggerRequest(true)
	response := initialTriggerResponse(t, request)
	response.HereBearer = "one-time-here-authority"
	var got controllerwire.CreateRunResponse
	var out bytes.Buffer
	err := submitTriggeredRun(t.Context(), request, false, "json", &out, triggerSubmission{
		Controller: initialTriggerController{create: func(_ context.Context, input controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			if input.IdempotencyKey == "" {
				t.Fatal("intake has no key")
			}
			return response, nil
		}},
		here:   func(_ context.Context, input controllerwire.CreateRunResponse) error { got = input; return nil },
		follow: func(context.Context, string) (string, error) { t.Fatal("here run followed twice"); return "", nil },
	})
	if err != nil || !reflect.DeepEqual(got, response) || strings.Contains(out.String(), response.HereBearer) || strings.Contains(out.String(), "request") {
		t.Fatalf("handoff=%+v receipt=%s error=%v", got, &out, err)
	}
}

func TestInitialTriggerPreflightHasNoIntake(t *testing.T) {
	for _, mode := range []string{"detached-here", "output", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			format := "json"
			if mode == "output" {
				format = "xml"
			}
			if mode == "cancelled" {
				cancel()
			}
			var out bytes.Buffer
			err := submitTriggeredRun(ctx, initialTriggerRequest(mode == "detached-here"), true, format, &out, triggerSubmission{
				Controller: initialTriggerController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
					t.Fatal("invalid intake")
					return controllerwire.CreateRunResponse{}, nil
				}},
			})
			if err == nil || out.Len() != 0 {
				t.Fatalf("output=%s error=%v", &out, err)
			}
		})
	}
}

func TestInitialTriggerRejectsInvalidReceiptsBeforeHosting(t *testing.T) {
	for _, change := range []func(*controllerwire.CreateRunResponse){
		func(r *controllerwire.CreateRunResponse) {},
		func(r *controllerwire.CreateRunResponse) { r.RunID = "" },
		func(r *controllerwire.CreateRunResponse) { r.Number = 0 },
		func(r *controllerwire.CreateRunResponse) { r.Request.Hash = "wrong" },
		func(r *controllerwire.CreateRunResponse) { r.Request.RunID = "original-retry-seal" },
		func(r *controllerwire.CreateRunResponse) { r.Request.Request.Pipeline = "other" },
		func(r *controllerwire.CreateRunResponse) { r.Replayed = true; r.HereBearer = "replayed-authority" },
		func(r *controllerwire.CreateRunResponse) {
			r.HereBearer = "unexpected-authority"
			r.Request.Request.Options.Runner.Value.Kind = runspec.RunnerAny
		},
	} {
		request := initialTriggerRequest(true)
		response := initialTriggerResponse(t, request)
		change(&response)
		var out bytes.Buffer
		err := submitTriggeredRun(t.Context(), request, false, "json", &out, triggerSubmission{
			Controller: initialTriggerController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
				return response, nil
			}},
			here: func(context.Context, controllerwire.CreateRunResponse) error {
				t.Fatal("invalid award reached handoff")
				return nil
			},
		})
		if err == nil || out.Len() != 0 {
			t.Fatalf("accepted malformed receipt: %s %v", &out, err)
		}
	}
}

func TestInitialTriggerFollowReturnsRecordedVerdict(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "cancelled", "running"} {
		t.Run(outcome, func(t *testing.T) {
			request := initialTriggerRequest(false)
			var out bytes.Buffer
			err := submitTriggeredRun(t.Context(), request, false, "json", &out, triggerSubmission{
				Controller: initialTriggerController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
					return initialTriggerResponse(t, request), nil
				}},
				follow: func(_ context.Context, run string) (string, error) {
					if run != "initial-run" {
						t.Fatal(run)
					}
					return outcome, nil
				},
			})
			if outcome == "success" && err != nil || outcome != "success" && err == nil || outcome == "running" && exitCodeFor(err) != 3 {
				t.Fatalf("outcome=%s receipt=%s error=%v", outcome, &out, err)
			}
			if outcome == "running" && strings.Contains(out.String(), `"outcome":"running"`) {
				t.Fatal("reported an unfinished outcome")
			}
		})
	}
}

func TestInitialTriggerIncompleteHereKeepsRunIdentity(t *testing.T) {
	request := initialTriggerRequest(true)
	response := initialTriggerResponse(t, request)
	response.HereBearer = "retain-this-bearer"
	var out bytes.Buffer
	err := submitTriggeredRun(t.Context(), request, false, "json", &out, triggerSubmission{Controller: initialTriggerController{
		create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			return response, nil
		},
	}})
	if exitCodeFor(err) != 3 || !strings.Contains(out.String(), `"run_id":"initial-run"`) || !strings.Contains(out.String(), `"error":`) || strings.Contains(out.String(), response.HereBearer) {
		t.Fatalf("receipt=%s error=%v", &out, err)
	}
}

func TestInitialTriggerCancellationRetainsReceivedHereAuthority(t *testing.T) {
	request := initialTriggerRequest(true)
	response := initialTriggerResponse(t, request)
	response.HereBearer = "received-private-bearer"
	credentials := hereCredentials{dir: t.TempDir()}
	options := client.EngineOptions{URL: "https://controller.example"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	err := submitTriggeredRun(ctx, request, false, "json", &out, triggerSubmission{
		Controller: initialTriggerController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			cancel()
			return response, nil
		}},
		here: func(ctx context.Context, response controllerwire.CreateRunResponse) error {
			return handoffHereRun(ctx, options, response, hereDependencies{
				credentials: func() (hereCredentials, error) { return credentials, nil },
				resolve: func(context.Context, client.EngineOptions) (hereRuntime, error) {
					t.Fatal("cancelled handoff started hosting")
					return hereRuntime{}, nil
				},
			})
		},
	})
	retained, loadErr := credentials.load(options.URL, response.RunID)
	if !errors.Is(err, context.Canceled) || exitCodeFor(err) != 3 || loadErr != nil || retained != response.HereBearer || !strings.Contains(out.String(), `"run_id":"initial-run"`) {
		t.Fatalf("received authority was lost: retained=%t load=%v error=%v receipt=%s", retained == response.HereBearer, loadErr, err, &out)
	}
}

func TestInitialTriggerFailuresPropagate(t *testing.T) {
	failed := errors.New("operation failed")
	for _, stage := range []string{"intake", "follow", "output"} {
		t.Run(stage, func(t *testing.T) {
			request := initialTriggerRequest(false)
			out := io.Writer(io.Discard)
			if stage == "output" {
				out = daemonEventsBrokenWriter{failed}
			}
			err := submitTriggeredRun(t.Context(), request, stage == "output", "plain", out, triggerSubmission{
				Controller: initialTriggerController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
					if stage == "intake" {
						return controllerwire.CreateRunResponse{}, failed
					}
					return initialTriggerResponse(t, request), nil
				}},
				follow: func(context.Context, string) (string, error) { return "success", failed },
			})
			if !errors.Is(err, failed) {
				t.Fatalf("lost failure: %v", err)
			}
		})
	}
}

func FuzzInitialTriggerDiagnosticsExcludePrivateInputs(f *testing.F) {
	f.Add("a-private-argument", "a-private-here-bearer", false)
	f.Add("a-private-argument", "a-private-here-bearer", true)
	f.Add("über\"/\n", "嵌入\"'/authority", true)
	f.Add("overlap", "argument-overlap-longer", true)
	f.Fuzz(func(t *testing.T, argument, bearer string, here bool) {
		if !utf8.ValidString(argument) || !utf8.ValidString(bearer) {
			t.Skip()
		}
		argument = "argument-" + argument
		bearer = "authority-" + bearer
		request := initialTriggerRequest(here)
		request.Request.RequestedArgs["password"] = argument
		response := initialTriggerResponse(t, request)
		if here {
			response.HereBearer = bearer
		}
		failure := &apierr.Error{Code: apierr.Unavailable, Message: fmt.Sprintf("failed with %s", argument), Hint: base64.StdEncoding.EncodeToString([]byte(argument)), Details: map[string]any{argument: bearer}}
		var out bytes.Buffer
		err := submitTriggeredRun(t.Context(), request, false, "json", &out, triggerSubmission{
			Controller: initialTriggerController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
				return response, nil
			}},
			here: func(context.Context, controllerwire.CreateRunResponse) error { return failure },
			follow: func(context.Context, string) (string, error) {
				failure.Details = map[string]any{argument: argument}
				return "", failure
			},
		})
		var canonical *apierr.Error
		if err == nil || !errors.As(err, &canonical) {
			t.Fatalf("error=%v", err)
		}
		raw, marshalErr := json.Marshal(canonical)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, value := range []string{argument, base64.StdEncoding.EncodeToString([]byte(argument))} {
			if strings.Contains(out.String()+err.Error()+string(raw), value) {
				t.Fatal("argument escaped masking")
			}
		}
		if here && strings.Contains(out.String()+err.Error()+string(raw), bearer) {
			t.Fatal("bearer escaped masking")
		}
		var receipt map[string]json.RawMessage
		if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		for key := range receipt {
			if !map[string]bool{"run_id": true, "number": true, "replayed": true, "error": true}[key] {
				t.Fatalf("private field: %s", key)
			}
		}
	})
}

func TestInitialHereHandoffClaimsAndRunsSharedHost(t *testing.T) {
	request := initialTriggerRequest(true)
	response := initialTriggerResponse(t, request)
	response.HereBearer = "private-initial-bearer"
	award := hereAward(t)
	award.RunID, award.Binding.RunID, award.Request = response.RunID, response.RunID, response.Request
	credentials := hereCredentials{dir: t.TempDir()}
	options := client.EngineOptions{URL: "https://controller.example"}
	var calls []string
	controller := initialTriggerController{
		Controller: hereTestController{claim: func(_ context.Context, claim controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			calls = append(calls, "claim")
			if claim.RunID != response.RunID || len(claim.Kinds) != 1 || claim.Kinds[0] != capability.UnitTrigger {
				t.Fatalf("claim = %+v", claim)
			}
			return controllerwire.ClaimResponse{Claim: &award}, nil
		}},
		create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			calls = append(calls, "intake")
			return response, nil
		},
	}
	deps := hereDependencies{
		credentials: func() (hereCredentials, error) { return credentials, nil },
		version:     "v1.2.3",
		resolve: func(ctx context.Context, opts client.EngineOptions) (hereRuntime, error) {
			retained, err := credentials.load(options.URL, response.RunID)
			presented, presentationErr := opts.Bearer(ctx)
			if err != nil || presentationErr != nil || retained != response.HereBearer || presented != retained {
				t.Fatalf("initial authority was not retained before claiming: %v %v", err, presentationErr)
			}
			return hereRuntime{controller: controller, bind: func(ctx context.Context, opts client.EngineOptions, unit controllerwire.ClaimedUnit) (hereHostSession, error) {
				calls = append(calls, "bind")
				presented, err := opts.Bearer(ctx)
				if err != nil || presented != award.Capability || unit.RunID != response.RunID {
					t.Fatalf("binding did not receive the award: %v", err)
				}
				return hereHostSession{host: hereTestHost(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
					calls = append(calls, "host")
					var sealed runspec.Request
					if err := json.Unmarshal(input.SealedRequest, &sealed); err != nil || sealed.Pipeline != request.Request.Pipeline || input.RunID != response.RunID || input.RequestHash != string(response.Request.Hash) || input.Accepted != nil || input.Owner.Owner.Holder != award.Binding.Holder {
						t.Fatalf("shared host received different inputs: %+v, %v", input, err)
					}
					return nodehost.RunCompletion{Outcome: "success"}, nil
				}), close: func() error { calls = append(calls, "close"); return nil }}, nil
			}}, nil
		},
	}
	var out bytes.Buffer
	err := submitTriggeredRun(t.Context(), request, false, "json", &out, triggerSubmission{
		Controller: controller,
		here: func(ctx context.Context, receipt controllerwire.CreateRunResponse) error {
			return handoffHereRun(ctx, options, receipt, deps)
		},
	})
	if err != nil || strings.Join(calls, ",") != "intake,claim,bind,host,close" {
		t.Fatalf("handoff calls = %v, error = %v", calls, err)
	}
	if strings.Contains(out.String(), response.HereBearer) || strings.Contains(out.String(), award.Capability) {
		t.Fatal("receipt exposed authority")
	}
}
