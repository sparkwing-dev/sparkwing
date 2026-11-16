package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestHereCompletionRetainsMissingSecretRepair(t *testing.T) {
	const repair = "set MISSING with sparkwing secrets set --secret MISSING --file <path> --shared"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(controllerwire.RunDetail{ID: "here-run", Status: "failed", Notices: []engineid.Notice{{Kind: engineid.NoticeSecretPreflightRefused, Reason: repair}}})
	}))
	defer server.Close()
	options := client.EngineOptions{URL: server.URL}
	completion := func(context.Context, client.EngineOptions, string) (nodehost.RunCompletion, error) {
		return nodehost.RunCompletion{Outcome: "failed"}, nil
	}
	for _, route := range []string{"resume", "handoff"} {
		t.Run(route, func(t *testing.T) {
			var err error
			if route == "resume" {
				err = runHereResumeCommand(t.Context(), triggerFlags{resume: "here-run", profile: "remote", output: "json"}, io.Discard,
					func(context.Context, string) (client.EngineOptions, func(), error) { return options, func() {}, nil },
					client.NewEngineClient, completion)
			} else {
				err = handoffHereRun(t.Context(), options, controllerwire.CreateRunResponse{RunID: "here-run"}, hereDependencies{
					credentials: func() (hereCredentials, error) { return hereCredentials{dir: t.TempDir()}, nil },
					attach: func(ctx context.Context, options client.EngineOptions, run string, _ func(context.Context) (nodehost.RunCompletion, error)) (nodehost.RunCompletion, error) {
						return completion(ctx, options, run)
					},
				})
			}
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != apierr.BadRequest || refusal.Message != repair {
				t.Fatalf("here configuration repair lost: %v", err)
			}
		})
	}
}

func TestLocalMissingSecretFinalErrorRetainsConfigurationRepair(t *testing.T) {
	const repair = "MISSING is unavailable in the shared secret scope; run sparkwing secrets set --secret MISSING --file <path> --shared"
	for _, format := range []string{"json", "plain"} {
		t.Run(format, func(t *testing.T) {
			var calls []string
			deps := localRuntimeFor(t, localTestAward(t), false, &calls)
			resolve := deps.resolve
			deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
				runtime, err := resolve(ctx, options)
				controller := runtime.controller.(localTestController)
				controller.read = func(context.Context, string) (controllerwire.RunDetail, error) {
					return controllerwire.RunDetail{ID: "local-run", Status: "failed", Notices: []engineid.Notice{{Kind: engineid.NoticeSecretPreflightRefused, Reason: repair}}}, nil
				}
				runtime.controller = controller
				return runtime, err
			}
			flags, args := parseRunFlagsArgs([]string{"--output=" + format, "--", "--password=private-value"})
			var output bytes.Buffer
			failure := runLocalCommand(t.Context(), "ci", flags, args, &output, deps)
			if failure == nil || exitCodeFor(failure) == 0 {
				t.Fatal("missing secret was accepted")
			}
			inv, err := clisurface.Parse("sparkwing", []string{"run", "ci", "-o", format})
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			if err := writeInvocationError(&diagnostic, &invocationError{inv: inv, cause: failure}, hosterr.WriteJSON); err != nil {
				t.Fatal(err)
			}
			message := diagnostic.String()
			if format == "json" {
				var envelope hosterr.Envelope
				if err := json.Unmarshal(diagnostic.Bytes(), &envelope); err != nil || envelope.Error.Code != apierr.BadRequest {
					t.Fatalf("configuration class lost: %s, %v", &diagnostic, err)
				}
				message = envelope.Error.Message
			}
			if !strings.Contains(message, repair) || strings.Contains(diagnostic.String(), "private-value") {
				t.Fatalf("final error lost the safe repair: %s", &diagnostic)
			}
		})
	}
}

func TestTriggeredMissingSecretFinalErrorRetainsConfigurationRepair(t *testing.T) {
	const repair = "MISSING is unavailable; run sparkwing secrets set --secret MISSING --file <path> --profile remote --shared"
	reader := &statusReader{details: []controllerwire.RunDetail{{ID: "remote-run", Status: "failed", Notices: []engineid.Notice{{Kind: engineid.NoticeSecretPreflightRefused, Reason: repair}}}}}
	var output bytes.Buffer
	outcome, err := followTriggeredRun(t.Context(), "remote-run", func(ctx context.Context, run string) (string, error) {
		return triggeredRunOutcome(ctx, reader, run, &output)
	})
	if outcome != "failed" || err == nil || exitCodeFor(err) == 0 {
		t.Fatalf("outcome=%q error=%v", outcome, err)
	}
	inv, parseErr := clisurface.Parse("sparkwing", []string{"pipeline", "trigger", "ci", "--profile", "remote", "-o", "json"})
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	var diagnostic bytes.Buffer
	if err := writeInvocationError(&diagnostic, &invocationError{inv: inv, cause: err}, hosterr.WriteJSON); err != nil {
		t.Fatal(err)
	}
	var envelope hosterr.Envelope
	if err := json.Unmarshal(diagnostic.Bytes(), &envelope); err != nil || envelope.Error.Code != apierr.BadRequest || envelope.Error.Message != repair {
		t.Fatalf("final error lost the scoped repair: %s, %v", &diagnostic, err)
	}
}

func TestInvalidIntegerFinalErrorNamesTypeAndRepair(t *testing.T) {
	for _, secret := range []bool{false, true} {
		input := localTestPreparation(t, localTestAward(t))
		input.declarations = declarationFixture(t, []nodewire.PipelineDescription{{Name: "CI", Args: []nodewire.ArgumentDescription{{Name: "n", Type: "int", Secret: secret}}}})
		flags, args := parseRunFlagsArgs([]string{"--output=json", "--", "--n=private-invalid-number"})
		_, failure := prepareLocalRunRequest(t.Context(), input, flags, args)
		if failure == nil {
			t.Fatal("invalid integer was accepted")
		}
		inv, err := clisurface.Parse("sparkwing", []string{"run", "ci", "-o", "json"})
		if err != nil {
			t.Fatal(err)
		}
		var diagnostic bytes.Buffer
		if err := writeInvocationError(&diagnostic, &invocationError{inv: inv, cause: failure}, hosterr.WriteJSON); err != nil {
			t.Fatal(err)
		}
		var envelope hosterr.Envelope
		if err := json.Unmarshal(diagnostic.Bytes(), &envelope); err != nil || envelope.Error.Code != apierr.InvalidArgs || !strings.Contains(envelope.Error.Message, "--n must be int") || !strings.Contains(envelope.Error.Hint, "--n=42") {
			t.Fatalf("secret=%t integer diagnostic lacks its repair: %s, %v", secret, &diagnostic, err)
		}
		if secret && strings.Contains(diagnostic.String(), "private-invalid-number") {
			t.Fatal("secret argument value escaped into the final error")
		}
		flags, args = parseRunFlagsArgs([]string{"--output=json", "--", "--n=42"})
		request, err := prepareLocalRunRequest(t.Context(), input, flags, args)
		if err != nil || request.Request.RequestedArgs["n"] != "42" {
			t.Fatalf("integer repair did not prepare a valid request: %v", err)
		}
	}
}
