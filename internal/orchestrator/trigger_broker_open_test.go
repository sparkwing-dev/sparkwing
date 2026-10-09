package orchestrator_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func declaredSecretTrigger(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "run-a", Pipeline: "build", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run-a", Pipeline: "build", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePlanSnapshot(ctx, "run-a",
		[]byte(`{"nodes":[],"secrets":[{"name":"DEPLOY_KEY","required":true}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOrReplaceSecret(store.Secret{Name: "DEPLOY_KEY", Value: "v-deploy", Pipeline: "build", Masked: true}, now); err != nil {
		t.Fatal(err)
	}
}

func brokeredSecretRead(t *testing.T, broker *orchestrator.TriggerBroker) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, broker.URL()+"/api/v1/secrets/DEPLOY_KEY?run=run-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+broker.Capability())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Value string `json:"value"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body.Value
}

// On an open controller a trigger claim carries no claim token and the runner
// no bearer, so the broker reads the run's declared secret the way the
// controller answers it directly: without a bearer.
func TestTriggerBrokerReadsSecretsFromAnOpenController(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	declaredSecretTrigger(t, st)
	trig, err := st.ClaimSpecificTrigger(context.Background(), "run-a", time.Minute)
	if err != nil || trig == nil || trig.ClaimToken != "" {
		t.Fatalf("an anonymous trigger claim = %+v, %v; want one with no claim token", trig, err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)
	broker, err := orchestrator.StartTriggerBroker(srv.URL, "", "", trig, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close(context.Background())
	if got, value := brokeredSecretRead(t, broker); got != http.StatusOK || value != "v-deploy" {
		t.Fatalf("a brokered secret read on an open controller = %d %q, want the declared secret", got, value)
	}
}

// An authenticating controller releases a secret only to a claim token, so a
// trigger claim that returned none reads nothing through the broker.
func TestTriggerBrokerRefusesASecretReadWithoutAClaimTokenWhenAuthenticating(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	declaredSecretTrigger(t, st)
	runner, _, err := st.CreateToken("runner-a", store.TokenKindRunner,
		[]string{controller.ScopeTriggersClaim, controller.ScopeRunsState}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	broker, err := orchestrator.StartTriggerBroker(srv.URL, "", runner,
		&store.Trigger{ID: "run-a", Pipeline: "build"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close(context.Background())
	if got, value := brokeredSecretRead(t, broker); got != http.StatusForbidden || value != "" {
		t.Fatalf("a tokenless brokered secret read on an authenticating controller = %d %q, want 403", got, value)
	}
}
