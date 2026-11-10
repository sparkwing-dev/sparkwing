//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestOptionalSecretIntake(t *testing.T) {
	const secret = "optional-private-token-3432"
	for _, tc := range []struct {
		name     string
		args     []string
		fallback *string
		invalid  bool
	}{
		{name: "omitted"},
		{name: "empty", args: []string{"--password="}},
		{name: "supplied", args: []string{"--password=" + secret}},
		{name: "default", fallback: new(secret)},
		{name: "absent classification", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := localTestPreparation(t, localTestAward(t))
			input.declarations = declarationFixture(t, []nodewire.PipelineDescription{{Name: "CI", Args: []nodewire.ArgumentDescription{{Name: "password", Type: "string", Secret: true, Default: tc.fallback}}}})
			request, err := prepareLocalRunRequest(t.Context(), input, runFlags{detached: true}, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if tc.invalid {
				request.Request.SecretArgs.Value = []string{"password"}
			}
			state, server, _ := burstIntakeController(t, "http://controller.test")
			server.WithPeerIdentity(func(*http.Request) capability.Subject {
				return capability.Subject{Kind: capability.SubjectLocal, ID: "uid:3432"}
			})
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body))

			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			if tc.invalid {
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "absent argument") {
					t.Fatalf("invalid classification HTTP %d: %s", w.Code, w.Body)
				}
				return
			}
			if w.Code != http.StatusCreated {
				t.Fatalf("intake HTTP %d: %s", w.Code, w.Body)
			}
			var created controllerwire.CreateRunResponse
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			tenant, err := state.ForTeam(t.Context(), store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), created.RunID)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := input.declarations.Resolve("CI", request.Request.RequestedArgs, request.Request.SecretArgs.Value)
			if err != nil {
				t.Fatal(err)
			}
			display := runspec.DisplayRequest(run.Request)
			encoded, err := json.Marshal(display)
			if err != nil || bytes.Contains(encoded, []byte(secret)) {
				t.Fatal("intake display leaked a secret")
			}
			if tc.name == "supplied" && display.RequestedArgs["password"] != "***" {
				t.Fatal("supplied secret was not masked")
			}
			if tc.name == "omitted" && len(resolved.SecretArgs) != 0 {
				t.Fatal("absent optional argument was classified")
			}
			if tc.fallback != nil && (resolved.Args["password"] != secret || len(resolved.SecretArgs) != 1) {
				t.Fatal("default secret lost its classification")
			}
		})
	}
}

func TestMissingRequiredSecretPreparation(t *testing.T) {
	input := localTestPreparation(t, localTestAward(t))
	input.declarations = declarationFixture(t, []nodewire.PipelineDescription{{Name: "CI", Args: []nodewire.ArgumentDescription{{Name: "password", Type: "string", Secret: true, Required: true}}}})
	_, err := prepareLocalRunRequest(t.Context(), input, runFlags{}, nil)
	var failure *apierr.Error
	if !errors.As(err, &failure) || hosterr.Status(failure.Code) != http.StatusBadRequest || !strings.Contains(failure.Message, "--password is required") {
		t.Fatalf("missing required secret refusal = %v", err)
	}
}
