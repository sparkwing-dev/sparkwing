package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func nativeLocalTransport(t testing.TB, bearer string) client.EngineOptions {
	t.Helper()
	return client.EngineOptions{URL: "https://local.example", HTTP: &http.Client{Transport: profileTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer "+bearer || !strings.HasSuffix(request.URL.Path, "/heartbeat") {
			t.Errorf("renewal lost its awarded binding: %s", request.URL.Path)
		}
		body, err := json.Marshal(controllerwire.RenewalResponse{ExpiresAt: time.Now().Add(time.Hour)})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, err
	})}}
}

func TestLocalHostRequestCapturesEnvironmentAndPipelineIdentity(t *testing.T) {
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	run := hostedRun{schema: nodeproto.PipelineDescription{Name: "deploy-prod", Args: []nodeproto.ArgumentDescription{{Name: "password", Secret: true}}},
		args: map[string]string{"password": "private-input"}, env: []string{"DOCKER_HOST=unix:///captured.sock", "SPARKWING_RUNNER_NAME=forged"},
		flags: runFlags{workers: 2, dryRun: true, noCache: true}, priority: "3"}
	run.priority, err = localHostPriority(t.Context(), "front", func(context.Context) (wingwire.QueueState, error) {
		return wingwire.QueueState{Waiters: []wingwire.Waiter{{Priority: 2}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := prepareLocalHostRequest(t.Context(), directory, nodehost.PipelineIdentity{Invoked: "deploy-prod", Entrypoint: "Deploy"}, run, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	input := request.Request
	if input.Origin != runspec.OriginLocal || input.Dispatch != runspec.DispatchHost || input.Pipeline != "deploy-prod" || input.Source.Entrypoint != "Deploy" || input.Source.Location.LocalCheckout != directory || input.Options.Runner.Value.Kind != runspec.RunnerAny || input.Options.Workers.Value != 2 || input.Options.Priority.Value != 3 || !input.Options.DryRun.Value || !input.Options.NoMemo.Value {
		t.Fatalf("local request lost its inputs: %+v", input)
	}
	if !reflect.DeepEqual(input.SecretArgs.Value, []string{"password"}) {
		t.Fatal("lost secret classification")
	}
	captured, err := snapshots.Load(t.Context(), input.Env)
	if err != nil || !strings.Contains(strings.Join(captured, "\n"), "DOCKER_HOST=unix:///captured.sock") {
		t.Fatalf("captured=%v error=%v", captured, err)
	}
	raw, _ := json.Marshal(input)
	if strings.Contains(string(raw), "captured.sock") || strings.Contains(string(raw), "forged") {
		t.Fatal("environment values entered the public seal")
	}
}

func TestLocalIntakeClaimsSharedHostAndPublishesHandle(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			request := initialTriggerRequest(false)
			request.Request.Origin = runspec.OriginLocal
			response := initialTriggerResponse(t, request)
			award := hereAward(t)
			award.RunID, award.Binding.RunID, award.Request = response.RunID, response.RunID, response.Request
			award.Purpose, award.Runner.Type = capability.PurposeTrigger, "local"
			options := nativeLocalTransport(t, award.Capability)
			handlePath := filepath.Join(t.TempDir(), "handle.json")
			calls := []string{}
			controller := initialTriggerController{
				Controller: hereTestController{claim: func(_ context.Context, got controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					calls = append(calls, "claim")
					if got.RunID != response.RunID || !reflect.DeepEqual(got.Kinds, []capability.UnitKind{capability.UnitTrigger}) {
						t.Fatalf("claim=%+v", got)
					}
					return controllerwire.ClaimResponse{Claim: &award}, nil
				}},
				create: func(_ context.Context, got controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
					calls = append(calls, "intake")
					if got.IdempotencyKey == "" {
						t.Fatal("missing intake idempotency")
					}
					if _, err := os.Stat(handlePath); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("published handle before intake")
					}
					return response, nil
				},
			}
			deps := hereDependencies{resolve: func(context.Context, client.EngineOptions) (runHostRuntime, error) {
				return runHostRuntime{controller: controller, bind: func(ctx context.Context, scoped client.EngineOptions, unit controllerwire.ClaimedUnit) (runHostSession, error) {
					calls = append(calls, "bind")
					bearer, err := scoped.Bearer(ctx)
					if err != nil || bearer != award.Capability || unit.RunID != response.RunID {
						t.Fatal("binder lost awarded authority")
					}
					return runHostSession{host: hereTestHost(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
						calls = append(calls, "host")
						if input.RunID != response.RunID || input.RequestHash != string(response.Request.Hash) || input.Owner.Owner.Holder != award.Binding.Holder || input.GrantedRunner.Type != "local" {
							t.Fatal("shared host lost award")
						}
						if _, err := os.Stat(handlePath); err != nil {
							t.Fatal("accepted run has no handle")
						}
						return nodehost.RunCompletion{Outcome: outcome}, nil
					}), close: func() error { calls = append(calls, "close"); return nil }}, nil
				}}, nil
			}}
			var output bytes.Buffer
			err := submitLocalHosted(t.Context(), options, request, handlePath, &output, deps)
			if (err == nil) != (outcome == "success") || strings.Join(calls, ",") != "intake,claim,bind,host,close" {
				t.Fatalf("calls=%v error=%v", calls, err)
			}
			if strings.TrimSpace(output.String()) != response.RunID {
				t.Fatalf("receipt=%q", output.String())
			}
		})
	}
}

func FuzzLocalHostUsesAwardedInputs(f *testing.F) {
	f.Add("forged-owner", int64(999))
	f.Add("", int64(-1))
	f.Fuzz(func(t *testing.T, forged string, generation int64) {
		award := hereAward(t)
		award.Purpose = capability.PurposeTrigger
		options := nativeLocalTransport(t, award.Capability)
		expected, err := nodehost.NewRunInput(award)
		if err != nil {
			t.Fatal(err)
		}
		closed, hosted := false, false
		runtime := runHostRuntime{bind: func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (runHostSession, error) {
			return runHostSession{input: nodehost.RunInput{RunID: forged, RequestHash: forged, SealedRequest: json.RawMessage(`{}`), Accepted: &nodehost.AcceptedRun{RunID: forged}, Owner: store.Actor{Kind: store.ActorOperator, Owner: store.OwnerLeaseRef{Holder: forged, Generation: generation}}, GrantedRunner: nodeproto.RunnerIdentity{Name: forged}},
				host: hereTestHost(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
					hosted = true
					if input.RunID != expected.RunID || input.RequestHash != expected.RequestHash || !bytes.Equal(input.SealedRequest, expected.SealedRequest) || input.Accepted != nil || input.Owner.Kind != expected.Owner.Kind || input.Owner.Owner != expected.Owner.Owner || !reflect.DeepEqual(input.GrantedRunner, expected.GrantedRunner) {
						t.Fatal("prepared host replaced the awarded seal or identity")
					}
					return nodehost.RunCompletion{Outcome: "success"}, nil
				}), close: func() error { closed = true; return nil }}, nil
		}}
		completion, err := hostLocalAward(t.Context(), options, award, runtime)
		if err != nil || !hosted || !closed || completion.Outcome != "success" {
			t.Fatalf("hosted=%v closed=%v completion=%+v error=%v", hosted, closed, completion, err)
		}
	})
}

func TestLocalHostMissingNativeBindingsDoesNotCreateOrPublish(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Unavailable, Message: "native binding unavailable"}
	handle := filepath.Join(t.TempDir(), "handle.json")
	var out bytes.Buffer
	err := submitLocalHosted(t.Context(), client.EngineOptions{}, controllerwire.CreateRunRequest{}, handle, &out, hereDependencies{resolve: func(context.Context, client.EngineOptions) (runHostRuntime, error) { return runHostRuntime{}, failure }})
	if !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("output=%q error=%v", out.String(), err)
	}
	if _, err := os.Stat(handle); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing runtime published a handle")
	}
}

func FuzzLocalNamedClaimCannotHostAnotherRun(f *testing.F) {
	f.Add("another-run")
	f.Add("")
	f.Fuzz(func(t *testing.T, requested string) {
		award := hereAward(t)
		if requested == award.RunID {
			return
		}
		invoked := false
		runtime := runHostRuntime{controller: hereTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			return controllerwire.ClaimResponse{Claim: &award}, nil
		}}, bind: func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (runHostSession, error) {
			invoked = true
			return runHostSession{}, nil
		}}
		err := hostLocalTrigger(t.Context(), client.EngineOptions{}, requested, func(context.Context, client.EngineOptions) (runHostRuntime, error) { return runtime, nil })
		var failure *apierr.Error
		if invoked || !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
			t.Fatalf("foreign named claim reached preparation: invoked=%v error=%v", invoked, err)
		}
	})
}
