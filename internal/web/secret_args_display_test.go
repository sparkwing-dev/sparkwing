package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	webSecretValue  = "s3cr3t-token-value"
	webVisibleValue = "prod"
)

func webSecretRun(id string) *store.Run {
	return &store.Run{
		ID:       id,
		Pipeline: "deploy",
		Args:     map[string]string{"token": webSecretValue, "env": webVisibleValue},
		Invocation: map[string]any{
			"args":                        map[string]string{"token": webSecretValue, "env": webVisibleValue},
			"reproducer":                  "sparkwing run deploy --env=" + webVisibleValue + " --token=" + webSecretValue,
			"inputs_hash":                 "sha256:legacy-oracle",
			store.InvocationSecretArgsKey: []string{"token"},
		},
	}
}

func assertWebRedacted(t *testing.T, surface, body string) {
	t.Helper()
	if strings.Contains(body, webSecretValue) {
		t.Errorf("%s sent the secret arg value to the browser:\n%s", surface, body)
	}
	if !strings.Contains(body, store.RedactedArgValue) {
		t.Errorf("%s carries no %s marker:\n%s", surface, store.RedactedArgValue, body)
	}
	if !strings.Contains(body, webVisibleValue) {
		t.Errorf("%s redacted the non-secret arg too:\n%s", surface, body)
	}
	if strings.Contains(body, "legacy-oracle") {
		t.Errorf("%s exposed a legacy secret input-hash oracle:\n%s", surface, body)
	}
}

func TestSecretArgs_ListRunsHandlerRedacts(t *testing.T) {
	t.Parallel()
	b := &fakeBackend{
		listRuns: func(store.RunFilter) ([]*store.Run, error) {
			return []*store.Run{webSecretRun("r1")}, nil
		},
	}
	rec := httptest.NewRecorder()
	ListRunsHandler(b)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	assertWebRedacted(t, "dashboard GET /api/v1/runs", rec.Body.String())
}

func TestSecretArgs_GetRunHandlerRedacts(t *testing.T) {
	t.Parallel()
	b := &fakeBackend{
		getRun:    func(id string) (*store.Run, error) { return webSecretRun(id), nil },
		listNodes: func(string) ([]*store.Node, error) { return []*store.Node{{NodeID: "n1"}}, nil },
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1", nil)
	req.SetPathValue("id", "r1")
	rec := httptest.NewRecorder()
	GetRunHandler(b)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	assertWebRedacted(t, "dashboard GET /api/v1/runs/{id}", rec.Body.String())

	reqNodes := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1?include=nodes", nil)
	reqNodes.SetPathValue("id", "r1")
	recNodes := httptest.NewRecorder()
	GetRunHandler(b)(recNodes, reqNodes)
	assertWebRedacted(t, "dashboard GET /api/v1/runs/{id}?include=nodes", recNodes.Body.String())
}

func TestSecretArgs_GetRunHandlerLeavesBackendRunUntouched(t *testing.T) {
	t.Parallel()
	shared := webSecretRun("r1")
	b := &fakeBackend{getRun: func(string) (*store.Run, error) { return shared, nil }}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1", nil)
	req.SetPathValue("id", "r1")
	GetRunHandler(b)(httptest.NewRecorder(), req)

	if shared.Args["token"] != webSecretValue {
		t.Errorf("handler mutated the shared run: Args[token] = %q", shared.Args["token"])
	}
}
