package controller

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestWriteErrorMasksInternalDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	private := "sql: users.token_prefix swtoken-private /private/db https://private-controller.invalid"
	writeError(rec, http.StatusInternalServerError, errors.New(private))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if body != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("body = %q", body)
	}
	for _, sentinel := range strings.Fields(private) {
		if strings.Contains(body, sentinel) {
			t.Errorf("internal response exposed %q: %s", sentinel, body)
		}
	}
}

func TestRunnerRoutesMaskDependencyFailuresProperty(t *testing.T) {
	inventory := &runnerInventoryFake{team: "a"}
	runners, member := runnerInventoryFixture(t, inventory)
	engine, claims, machine := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	for _, tc := range []struct {
		method, path, body, raw string
		handler                 http.Handler
		fail                    func(error)
	}{
		{"GET", "/api/v1/runners", "", member, runners, func(err error) { inventory.err = err }},
		{"POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"]}`, machine, claims, func(err error) {
			engine.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return err })
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			property := func(input []byte) bool {
				private := "private-dependency-" + hex.EncodeToString(input)
				tc.fail(errors.New(private))
				r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+tc.raw)
				w := httptest.NewRecorder()
				tc.handler.ServeHTTP(w, r)
				var body apierr.Envelope
				if w.Code != http.StatusInternalServerError || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error == nil || body.Error.Code != apierr.Internal || strings.Contains(w.Body.String(), private) {
					t.Logf("dependency failure: %d %s", w.Code, w.Body)
					return false
				}
				return w.Header().Get("Cache-Control") == "no-store"
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
