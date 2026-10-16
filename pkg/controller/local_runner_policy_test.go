package controller

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func FuzzMachineClaimsCannotSelectLocalVersionPolicy(f *testing.F) {
	f.Add("(devel)", "type=local")
	f.Add("v1.2.3", "local:1000")
	f.Fuzz(func(t *testing.T, version, label string) {
		e, handler, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
		checked, exempted := false, false
		e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error {
			checked = true
			return &apierr.Error{Code: apierr.Unavailable, Message: "verified policy unavailable"}
		})
		e.LocalRunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { exempted = true; return nil })
		body, err := json.Marshal(map[string]any{"runner_version": "version-" + version, "kinds": []string{"node"}, "labels": []string{label, "type=local"}, "available_cores": 1})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/api/v1/runners/claim", strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer "+raw)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if exempted || !checked || response.Code != 503 {
			t.Fatalf("machine claim selected local policy: checked=%t exempted=%t response=%d %s", checked, exempted, response.Code, response.Body.String())
		}
	})
}
