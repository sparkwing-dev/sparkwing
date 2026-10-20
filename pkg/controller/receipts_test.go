package controller_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestReceiptEndpoint_RoundTrip(t *testing.T) {
	f := newBrowserFixture(t, nil)
	st := f.st
	st.SetIntakePolicy(receiptPolicy{})
	tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(f.sessID))
	admitted, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{
		Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])},
		Request: runspec.Request{
			Version: 1, Team: string(store.DefaultTeam), Pipeline: "deploy", Mode: runspec.IntakeExecute,
			Source:   runspec.Source{Ref: strings.Repeat("a", 40)},
			Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)},
			RequestedArgs: map[string]string{"env": "prod"}, SecretArgs: runspec.Optional[[]string]{Set: true, Value: []string{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	mustExec(t, st, `UPDATE runs SET status='success', started_at=?, finished_at=? WHERE id=?`, start.UnixNano(), end.UnixNano(), admitted.RunID)
	for _, job := range []struct {
		id         string
		start, end time.Time
		spec       string
	}{
		{"build", start, start.Add(time.Hour), `{"id":"build"}`},
		{"deploy", start.Add(time.Hour), end, `{"id":"deploy","deps":["build"]}`},
	} {
		mustExec(t, st, `INSERT INTO nodes(team,run_id,node_id,status,outcome,started_at,finished_at,spec_json,execution_json) VALUES ('default',?,?,'done','success',?,?,?,?)`,
			admitted.RunID, job.id, job.start.UnixNano(), job.end.UnixNano(), []byte(job.spec), []byte(`{}`))
	}

	handler := f.srv.
		WithCostRate(0.05, "controller config (cost_per_runner_hour=$0.05)").
		Handler()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+admitted.RunID+"/receipt", nil)
	request.Header.Set("Authorization", "Bearer "+f.token)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("receipt: %d %s", response.Code, response.Body)
	}
	body := response.Body.Bytes()
	var rec map[string]any
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, body)
	}
	for _, key := range []string{"run_id", "pipeline", "git_sha", "status", "started_at", "finished_at", "duration_ms", "identity", "steps", "cost", "receipt_sha"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("missing top-level field %q", key)
		}
	}
	id, _ := rec["identity"].(map[string]any)
	for _, key := range []string{"pipeline_version_hash", "inputs_hash", "plan_hash", "outputs_hash"} {
		v, ok := id[key]
		if !ok || v == nil {
			t.Errorf("identity.%s missing", key)
		}
	}
	cost, _ := rec["cost"].(map[string]any)
	if cost["currency"] != "USD" {
		t.Errorf("cost.currency = %v, want USD", cost["currency"])
	}
	if cents, _ := cost["compute_cents"].(float64); cents != 10 {
		t.Errorf("cost.compute_cents = %v, want 10", cost["compute_cents"])
	}
	if got, _ := cost["rate_source"].(string); !strings.Contains(got, "cost_per_runner_hour") {
		t.Errorf("cost.rate_source = %q, missing rate provenance", got)
	}
	if settled, _ := cost["settled"].(bool); settled {
		t.Error("cost.settled should default false")
	}
	if sha, _ := rec["receipt_sha"].(string); !strings.HasPrefix(sha, "sha256:") {
		t.Errorf("receipt_sha = %q, want sha256: prefix", sha)
	}
}

func TestReceiptEndpoint_NotFound(t *testing.T) {
	f := newBrowserFixture(t, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs/missing/receipt", nil)
	request.Header.Set("Authorization", "Bearer "+f.token)
	response := f.serve(request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", response.Code, response.Body)
	}
}

func mustExec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := teststore.SQL(t, st).ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

type receiptPolicy struct{}

func (receiptPolicy) ResolveIntake(_ context.Context, _ capability.AuthorityResolver, request store.IntakeRequest) (store.IntakePolicyFacts, error) {
	return store.IntakePolicyFacts{Options: &request.Request.Options, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
}
