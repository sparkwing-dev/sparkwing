package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type optionalSecretServices struct {
	sdkserve.Services
	manager controllerwire.SecretManager
}

func (s optionalSecretServices) Secret(ctx context.Context, name string) (string, bool, error) {
	value, err := s.manager.ReadSecret(ctx, controllerwire.SecretSelector{Name: name})
	return value.Value, value.Masked, err
}

func TestSecrets_OptionalUnavailableReadsAreMissing(t *testing.T) {
	for _, scope := range []string{"absent", "other pipeline", "other repository", "unshared", "uploaded source"} {
		t.Run(scope, func(t *testing.T) {
			f := newBrowserFixture(t, nil)
			if scope != "absent" {
				write := map[string]any{"name": "TOKEN", "value": "private-value", "repo": "gh:1", "pipeline": "ci"}
				switch scope {
				case "other pipeline":
					write["pipeline"] = "deploy"
				case "other repository":
					write["repo"] = "gh:2"
				case "unshared":
					delete(write, "repo")
					delete(write, "pipeline")
				}
				if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", write); code != http.StatusOK {
					t.Fatalf("write: HTTP %d %s", code, body)
				}
			}
			claim := secretClaimAs(t, f, store.DefaultTeam, "ci", scope != "uploaded source", "TOKEN")
			tenant := teststore.ForTeam(t, f.st, store.DefaultTeam)
			run, err := tenant.ReadRun(t.Context(), string(store.DefaultTeam)+"-ci")
			if err != nil {
				t.Fatal(err)
			}
			run.Spec.Resolved.OptionalSecretNames = []string{"TOKEN"}
			spec, err := json.Marshal(run.Spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `UPDATE runs SET spec_json=? WHERE team=? AND id=?`, spec, store.DefaultTeam, run.ID); err != nil {
				t.Fatal(err)
			}
			manager, err := client.NewSecretManager(client.EngineOptions{
				URL: "http://controller", HTTP: &http.Client{Transport: childOutputTransport{handler: f.h}},
				Bearer: func(context.Context) (string, error) { return strings.TrimPrefix(claim, "Bearer "), nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := sdkserve.DefaultOperations.Context(t.Context(), nodeproto.RunContext{RunID: run.ID}, nodeproto.RunnerIdentity{}, optionalSecretServices{manager: manager})
			if value, err := sparkwing.Secret(ctx, "TOKEN"); value != "" || !errors.Is(err, sparkwing.ErrSecretMissing) {
				t.Fatalf("Go optional secret: value=%q error=%v", value, err)
			}
			code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/TOKEN", claim, nil)
			var missing apierr.Envelope
			if code != http.StatusNotFound || json.Unmarshal([]byte(body), &missing) != nil || missing.Error == nil || missing.Error.Code != apierr.NotFound || strings.Contains(body, "private-value") || strings.Contains(body, "deploy") || strings.Contains(body, "gh:2") {
				t.Fatalf("unavailable optional secret: HTTP %d %s", code, body)
			}
			code, body, _ = secretsRequest(t, f, "GET", "/api/v1/secrets/UNDECLARED", claim, nil)
			var refused apierr.Envelope
			if code != http.StatusForbidden || json.Unmarshal([]byte(body), &refused) != nil || refused.Error == nil || refused.Error.Details["reason"] != string(apierr.ReasonSecretUndeclared) {
				t.Fatalf("undeclared secret: HTTP %d %s", code, body)
			}
		})
	}
}
