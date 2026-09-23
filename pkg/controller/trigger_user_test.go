package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newTriggerUserServer(t *testing.T) (*store.Store, *httptest.Server, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, _, err := st.CreateToken("alice", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(ts.Close)
	return st, ts, raw
}

func TestTrigger_UserIsTheAuthenticatedPrincipal(t *testing.T) {
	st, ts, raw := newTriggerUserServer(t)
	ctx := context.Background()

	resp, err := client.NewWithToken(ts.URL, nil, raw).CreateTrigger(ctx, client.TriggerRequest{
		Pipeline: "demo",
		Trigger:  client.TriggerMeta{Source: "manual"},
	})
	if err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	trig, err := st.GetTrigger(ctx, resp.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if trig.TriggerUser != "alice" {
		t.Fatalf("trigger user = %q, want the token's principal alice", trig.TriggerUser)
	}
}

func TestTrigger_RefusesAUserNamedInTheBody(t *testing.T) {
	st, ts, raw := newTriggerUserServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/triggers",
		strings.NewReader(`{"pipeline":"demo","trigger":{"source":"manual","user":"mallory"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a body that names its own user", resp.StatusCode)
	}
	runs, err := st.ListRuns(context.Background(), store.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("refused submission still created %d run(s)", len(runs))
	}
}
