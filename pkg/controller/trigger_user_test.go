package controller_test

import (
	"context"
	"encoding/json"
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

// Released CLIs still send trigger.user, and a CLI and a controller upgrade
// independently, so the field is accepted and never trusted.
func TestTrigger_IgnoresAUserNamedInTheBody(t *testing.T) {
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
	var created struct {
		RunID string `json:"run_id"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 || decodeErr != nil || created.RunID == "" {
		t.Fatalf("status = %d (%v, run %q), want a released CLI's body accepted", resp.StatusCode, decodeErr, created.RunID)
	}
	trig, err := st.GetTrigger(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if trig.TriggerUser != "alice" {
		t.Fatalf("trigger user = %q, want the token's principal alice, never the body's mallory", trig.TriggerUser)
	}
}
