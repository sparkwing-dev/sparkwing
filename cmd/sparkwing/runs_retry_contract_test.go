package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type retryController struct {
	controllerwire.Controller
	retry func(context.Context, string, controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error)
}

func (c retryController) Retry(ctx context.Context, id string, input controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
	return c.retry(ctx, id, input)
}

func retryResponse(id string) controllerwire.CreateRunResponse {
	return controllerwire.CreateRunResponse{RunID: id, Number: 413, Request: runspec.SealedRequest{RunID: "original-run"}}
}

func TestRetryCommandSelectsModeAndControllerBeforeIntake(t *testing.T) {
	for _, profile := range []string{"", "prod"} {
		for _, mode := range []string{"failed", "all-jobs"} {
			t.Run(profile+"/"+mode, func(t *testing.T) {
				var calls []string
				var keys []string
				closed := false
				c := retryController{retry: func(ctx context.Context, id string, input controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
					calls = append(calls, id)
					want := controllerwire.RetryFailed
					if mode == "all-jobs" {
						want = controllerwire.RetryAll
					}
					if input.Mode != want || input.IdempotencyKey == "" || ctx != t.Context() {
						t.Fatalf("retry input: %+v", input)
					}
					keys = append(keys, input.IdempotencyKey)
					return retryResponse("new-" + id), nil
				}}
				resolve := func(_ context.Context, selected string) (controllerwire.Controller, func(), error) {
					if selected != profile {
						t.Fatalf("profile = %q", selected)
					}
					return c, func() { closed = true }, nil
				}
				args := []string{"--run", "run-a", "--run", "-", "--" + mode, "-o", "plain"}
				if profile != "" {
					args = append(args, "--profile", profile)
				}
				var out bytes.Buffer
				if err := runRunsRetryCommand(t.Context(), args, strings.NewReader("run-a\nrun-b\n"), &out, resolve); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(calls, []string{"run-a", "run-b"}) || keys[0] == keys[1] || !closed || out.String() != "new-run-a\nnew-run-b\n" {
					t.Fatalf("calls=%v keys=%v closed=%v output=%q", calls, keys, closed, out.String())
				}
			})
		}
	}
}

func TestRetryCommandRefusesInvalidSelectionBeforeConnection(t *testing.T) {
	for _, args := range [][]string{
		{"--run", "a"}, {"--run", "a", "--failed", "--all-jobs"},
		{"--failed"}, {"a", "--failed"}, {"--run", "a", "--failed", "-o", "csv"},
		{"--run", "a", "--failed=false"}, {"--run", "-", "--failed"},
	} {
		var out bytes.Buffer
		resolve := func(context.Context, string) (controllerwire.Controller, func(), error) {
			t.Fatal("invalid invocation connected")
			return nil, nil, nil
		}
		err := runRunsRetryCommand(t.Context(), args, strings.NewReader("\n"), &out, resolve)
		if exitCodeFor(err) != 2 || out.Len() != 0 {
			t.Fatalf("args=%v output=%q err=%v", args, out.String(), err)
		}
	}
}

func TestRetryCommandHelpAndCancellationHaveNoSideEffects(t *testing.T) {
	resolve := func(context.Context, string) (controllerwire.Controller, func(), error) {
		t.Fatal("connected")
		return nil, nil, nil
	}
	var out bytes.Buffer
	if err := runRunsRetryCommand(t.Context(), []string{"--help"}, nil, &out, resolve); err != nil || out.Len() == 0 {
		t.Fatalf("help: %v %q", err, out.String())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runRunsRetryCommand(ctx, []string{"--run", "a", "--failed"}, nil, io.Discard, resolve); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestRetryConnectionFailureDoesNotSubmit(t *testing.T) {
	refusal := &apierr.Error{Code: apierr.Unavailable, Message: "local controller unavailable"}
	resolve := func(context.Context, string) (controllerwire.Controller, func(), error) {
		return nil, nil, refusal
	}
	var out bytes.Buffer
	err := runRunsRetryCommand(t.Context(), []string{"--run", "source", "--failed"}, nil, &out, resolve)
	if !errors.Is(err, refusal) || out.Len() != 0 {
		t.Fatalf("output=%q err=%v", out.String(), err)
	}
}

func TestRetryReportsPartialFailureAndKeepsReplay(t *testing.T) {
	c := retryController{retry: func(_ context.Context, id string, _ controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
		if id == "a" {
			return controllerwire.CreateRunResponse{}, &apierr.Error{Code: apierr.Forbidden, Message: "retry refused", Hint: "ask a team admin"}
		}
		result := retryResponse("new-b")
		result.Replayed = true
		return result, nil
	}}
	var out bytes.Buffer
	err := retryRunIDs(t.Context(), c, []string{"a", "b"}, controllerwire.RetryFailed, &out, "json")
	if err == nil || !strings.Contains(err.Error(), "retry refused") {
		t.Fatalf("failure lost: %v", err)
	}
	for _, text := range []string{`"source_run_id":"a"`, `"code":"forbidden"`, `"hint":"ask a team admin"`, `"run_id":"new-b"`, `"number":413`, `"replayed":true`} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %s in %s", text, &out)
		}
	}
}

func TestRetryStopsAfterCancellationOrOutputFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		c := retryController{retry: func(context.Context, string, controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
			calls++
			if cancelled {
				cancel()
			}
			return retryResponse("new-a"), nil
		}}
		out := io.Writer(retryBrokenWriter{})
		if cancelled {
			out = io.Discard
		}
		err := retryRunIDs(ctx, c, []string{"a", "b"}, controllerwire.RetryAll, out, "plain")
		if err == nil || calls != 1 {
			t.Fatalf("cancelled=%v calls=%d err=%v", cancelled, calls, err)
		}
	}
}

type retryBrokenWriter struct{}

func (retryBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRetryRejectsMalformedReceipt(t *testing.T) {
	for _, result := range []controllerwire.CreateRunResponse{
		{}, {RunID: "r", Number: 1}, {RunID: "r", Number: 0, Request: runspec.SealedRequest{RunID: "r"}},
	} {
		c := retryController{retry: func(context.Context, string, controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
			return result, nil
		}}
		var out bytes.Buffer
		err := retryRunIDs(t.Context(), c, []string{"a"}, controllerwire.RetryAll, &out, "plain")
		if err == nil || out.Len() != 0 {
			t.Fatalf("malformed receipt succeeded: %+v output=%q err=%v", result, out.String(), err)
		}
	}
}

func TestRetryReceiptKeepsOriginalSealedIdentity(t *testing.T) {
	response := retryResponse("new-retry")
	response.Request.Hash = "original-seal"
	response.Request.Request.Lineage.RetryOf = "earlier-source"
	response.Request.Request.Actor = runspec.Actor{Kind: "account", ID: "original-actor"}
	before := response
	got, err := retryReceipt("latest-source", response)
	if err != nil || got.SourceRunID != "latest-source" || got.RunID != "new-retry" || !reflect.DeepEqual(response, before) {
		t.Fatalf("sealed identity was replaced: output=%+v error=%v", got, err)
	}
}

func FuzzRetryReceiptsExcludeExecutionInputs(f *testing.F) {
	f.Add("private-retry-credential", "private-argument-value", false)
	f.Add("private-retry-credential", "private-argument-value", true)
	f.Fuzz(func(t *testing.T, bearer, argument string, here bool) {
		result := retryResponse("retry-run")
		result.Request.Request.RequestedArgs = map[string]string{"password": argument}
		if here {
			result.Request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
			result.HereBearer = bearer
		}
		render := func(response controllerwire.CreateRunResponse) (string, string) {
			c := retryController{retry: func(context.Context, string, controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
				return response, nil
			}}
			var out bytes.Buffer
			err := retryRunIDs(t.Context(), c, []string{"source-run"}, controllerwire.RetryFailed, &out, "json")
			if here && exitCodeFor(err) != 3 || !here && err != nil {
				t.Fatalf("here=%v error=%v", here, err)
			}
			diagnostic := ""
			if err != nil {
				diagnostic = err.Error()
			}
			return out.String(), diagnostic
		}
		out, diagnostic := render(result)
		baseline := result
		baseline.Request.Request.RequestedArgs = map[string]string{"password": "baseline"}
		if here {
			baseline.HereBearer = "baseline-bearer"
		}
		publicOutput, publicDiagnostic := render(baseline)
		if out != publicOutput || diagnostic != publicDiagnostic {
			t.Fatal("private inputs changed public output or diagnostic")
		}
		var public map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &public); err != nil {
			t.Fatal(err)
		}
		allowed := map[string]bool{"source_run_id": true, "run_id": true, "number": true, "replayed": true, "error": true}
		for field := range public {
			if !allowed[field] {
				t.Fatalf("private response field serialized: %s", field)
			}
		}
	})
}
