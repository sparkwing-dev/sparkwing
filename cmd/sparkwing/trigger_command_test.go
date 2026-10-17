package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestTriggerCommandSubmitsPreparedSealThroughFinalRoute(t *testing.T) {
	enterGitCheckout(t, "https://git.example/acme/app.git")
	prepared := initialTriggerRequest(false)
	prepared.IdempotencyKey = "one-intake"
	response := initialTriggerResponse(t, prepared)
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	calls, closed := 0, false
	err = runTriggerCommand(t.Context(), []string{"deploy-prod", "--profile=prod", "--detach", "-o=json", "--", "--image=release"}, &out,
		func(_ context.Context, flags triggerFlags) (controllerwire.CreateRunRequest, error) {
			if flags.pipeline != "deploy-prod" || flags.profile != "prod" || !reflect.DeepEqual(flags.passthrough, []string{"--image=release"}) {
				t.Fatalf("preparation input: %+v", flags)
			}
			return prepared, nil
		}, func(_ context.Context, profile string) (client.EngineOptions, func(), error) {
			if profile != "prod" {
				t.Fatalf("profile=%s", profile)
			}
			return client.EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: profileTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "POST" || req.URL.Path != "/api/v1/runs" || req.Header.Get("Idempotency-Key") != "one-intake" {
					t.Fatalf("intake: %s %s", req.Method, req.URL)
				}
				var got controllerwire.CreateRunRequest
				if err := json.NewDecoder(req.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, prepared) {
					t.Fatalf("request=%+v err=%v", got, err)
				}
				return &http.Response{StatusCode: 201, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
			})}}, func() { closed = true }, nil
		})
	if err != nil || calls != 1 || !closed || out.String() != "{\"run_id\":\"initial-run\",\"number\":17,\"replayed\":false}\n" {
		t.Fatalf("calls=%d closed=%v output=%s err=%v", calls, closed, &out, err)
	}
}

func TestTriggerCommandPreparationFailureCannotSubmit(t *testing.T) {
	enterGitCheckout(t, "https://git.example/acme/app.git")
	failure := errors.New("source unavailable")
	var out bytes.Buffer
	err := runTriggerCommand(t.Context(), []string{"deploy-prod", "--profile=prod", "--detach", "-o=json"}, &out,
		func(context.Context, triggerFlags) (controllerwire.CreateRunRequest, error) {
			return controllerwire.CreateRunRequest{}, failure
		},
		func(context.Context, string) (client.EngineOptions, func(), error) {
			t.Fatal("failed preparation reached controller")
			return client.EngineOptions{}, nil, nil
		})
	if !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("output=%s err=%v", &out, err)
	}
}

func TestNativeTriggerRefusesIncompleteSourceBeforeIntake(t *testing.T) {
	for _, flags := range []triggerFlags{{workingTree: true}, {allowSecretFiles: []string{"private.env"}}} {
		request, err := prepareNativeTrigger(t.Context(), flags)
		if err == nil || request.IdempotencyKey != "" || request.Request.Pipeline != "" || strings.Contains(err.Error(), "private.env") {
			t.Fatalf("request=%+v error=%v", request, err)
		}
	}
}
