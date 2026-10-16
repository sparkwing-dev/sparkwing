package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const (
	ctlSecretValue  = "s3cr3t-token-value"
	ctlVisibleValue = "prod"
)

func seedSecretArgRun(t *testing.T, st *store.Store, id string) {
	t.Helper()
	seedSecretArgRunAs(t, st, id, "failed")
}

func seedSecretArgRunAs(t *testing.T, st *store.Store, id, status string) {
	t.Helper()
	err := teststore.SeedRun(st, context.Background(), store.DefaultTeam, store.Run{
		ID:        id,
		Pipeline:  "deploy",
		Status:    status,
		GitBranch: "main",
		GitSHA:    "abc123",
		Args:      map[string]string{"token": ctlSecretValue, "env": ctlVisibleValue},
		Invocation: map[string]any{
			"args":       map[string]string{"token": ctlSecretValue, "env": ctlVisibleValue},
			"reproducer": "sparkwing run deploy --env=" + ctlVisibleValue + " --token=" + ctlSecretValue,
			"flags":      map[string]any{"full": true},

			store.InvocationSecretArgsKey: []string{"token"},
		},
		StartedAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
}

func secretArgController(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)
	return st, srv
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status=%d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertRedactedResponse(t *testing.T, surface, body string) {
	t.Helper()
	if strings.Contains(body, ctlSecretValue) {
		t.Errorf("%s served the secret arg value to the client:\n%s", surface, body)
	}
	if !strings.Contains(body, runspec.RedactedArgValue) {
		t.Errorf("%s carries no %s marker:\n%s", surface, runspec.RedactedArgValue, body)
	}
	if !strings.Contains(body, ctlVisibleValue) {
		t.Errorf("%s redacted the non-secret arg too:\n%s", surface, body)
	}
}

func TestSecretArgs_ControllerListRunsRedacts(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRun(t, st, "run-1")
	assertRedactedResponse(t, "GET /api/v1/runs", getBody(t, srv.URL+"/api/v1/runs"))
}

func TestSecretArgs_ControllerReceiptRedacts(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRun(t, st, "run-1")
	if _, err := st.DB().Exec(`UPDATE runs SET invocation_json = json_set(invocation_json, '$.inputs_hash', 'sha256:offline-oracle') WHERE id = 'run-1'`); err != nil {
		t.Fatal(err)
	}
	body := getBody(t, srv.URL+"/api/v1/runs/run-1/receipt")
	if strings.Contains(body, ctlSecretValue) {
		t.Errorf("receipt served the secret arg value:\n%s", body)
	}
	if !strings.Contains(body, runspec.RedactedArgValue) {
		t.Errorf("receipt carries no redaction marker:\n%s", body)
	}
	if strings.Contains(body, "offline-oracle") {
		t.Errorf("receipt served a stored input-hash oracle:\n%s", body)
	}
	var rec struct {
		Identity struct {
			InputsHash string `json:"inputs_hash"`
		} `json:"identity"`
	}
	if err := json.Unmarshal([]byte(body), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Identity.InputsHash != "" {
		t.Errorf("receipt identity inputs_hash = %q, want empty for secret arguments", rec.Identity.InputsHash)
	}
}

func TestSecretArgs_ControllerRejectsOlderWriterInputHash(t *testing.T) {
	st, srv := secretArgController(t)
	body, err := json.Marshal(store.Run{
		ID: "old-writer", Pipeline: "deploy", Status: "running", StartedAt: time.Now(),
		Args: map[string]string{"token": ctlSecretValue},
		Invocation: map[string]any{
			"args":                        map[string]string{"token": ctlSecretValue},
			"inputs_hash":                 "sha256:offline-oracle",
			store.InvocationSecretArgsKey: []string{"token"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/api/v1/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/v1/runs status = %d, want 400: %s", resp.StatusCode, got)
	}
	if _, err := teststore.ForTeam(t, st, store.DefaultTeam).GetRun(context.Background(), "old-writer"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun error = %v, want ErrNotFound", err)
	}
}

func TestSecretArgs_ControllerPipelineLatestRedacts(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRunAs(t, st, "run-1", "pending")
	if err := teststore.SeedRunFinish(st, context.Background(), store.DefaultTeam, "run-1", "success", ""); err != nil {
		t.Fatal(err)
	}
	assertRedactedResponse(t, "GET /api/v1/pipelines/{name}/latest",
		getBody(t, srv.URL+"/api/v1/pipelines/deploy/latest"))
}

func TestSecretArgs_ControllerAttemptsRedacts(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRun(t, st, "run-1")
	assertRedactedResponse(t, "GET /api/v1/runs/{id}/attempts",
		getBody(t, srv.URL+"/api/v1/runs/run-1/attempts"))
}

func TestSecretArgs_RetryStillReceivesPlaintext(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRun(t, st, "run-1")

	resp, err := http.Post(srv.URL+"/api/v1/runs/run-1/retry", "application/json", bytes.NewBufferString(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry status=%d, want 202", resp.StatusCode)
	}

	ctx := context.Background()
	src, err := teststore.ForTeam(t, st, store.DefaultTeam).GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if src.Args["token"] != ctlSecretValue {
		t.Fatalf("source run args were mutated by a read path: %q", src.Args["token"])
	}
	if src.RetriedAs == "" {
		t.Fatal("retry did not record RetriedAs")
	}
	retried, err := teststore.ForTeam(t, st, store.DefaultTeam).GetRun(ctx, src.RetriedAs)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Args["token"] != ctlSecretValue {
		t.Errorf("retry run args[token] = %q, want plaintext %q -- the retry would execute with a literal %s",
			retried.Args["token"], ctlSecretValue, runspec.RedactedArgValue)
	}
	trig, err := teststore.ForTeam(t, st, store.DefaultTeam).GetTrigger(ctx, src.RetriedAs)
	if err != nil {
		t.Fatal(err)
	}
	if trig.Args["token"] != ctlSecretValue {
		t.Errorf("retry trigger args[token] = %q, want plaintext", trig.Args["token"])
	}

	if got, _ := retried.Invocation[store.InvocationSecretArgsKey].([]any); len(got) != 1 || got[0] != "token" {
		t.Fatalf("retry run classification = %v, want [token]", got)
	}
	assertRedactedResponse(t, "GET /api/v1/runs/{id} for a pending retry",
		getBody(t, srv.URL+"/api/v1/runs/"+src.RetriedAs))
}

func TestSecretArgs_ExecutionViewDoesNotWidenOtherEndpoints(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRunAs(t, st, "run-1", "pending")
	if err := teststore.SeedRunFinish(st, context.Background(), store.DefaultTeam, "run-1", "success", ""); err != nil {
		t.Fatal(err)
	}
	const q = "?include=" + "secret_values"
	assertRedactedResponse(t, "GET /api/v1/runs"+q, getBody(t, srv.URL+"/api/v1/runs"+q))
	assertRedactedResponse(t, "GET /api/v1/runs/{id}/attempts"+q,
		getBody(t, srv.URL+"/api/v1/runs/run-1/attempts"+q))
	assertRedactedResponse(t, "GET /api/v1/pipelines/{name}/latest"+q,
		getBody(t, srv.URL+"/api/v1/pipelines/deploy/latest"+q))
	assertRedactedResponse(t, "GET /api/v1/runs/{id}/receipt"+q,
		getBody(t, srv.URL+"/api/v1/runs/run-1/receipt"+q))
}
