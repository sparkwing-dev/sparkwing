package controller_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	ctlSecretValue  = "s3cr3t-token-value"
	ctlVisibleValue = "prod"
)

// seedSecretArgRun writes a run shaped exactly as the orchestrator
// writes one for a pipeline with a `secret:"true"` input: plaintext in
// the row and the invocation, with the classification alongside.
func seedSecretArgRun(t *testing.T, st *store.Store, id string) {
	t.Helper()
	err := st.CreateRun(context.Background(), store.Run{
		ID:        id,
		Pipeline:  "deploy",
		Status:    "failed",
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
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
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

// assertRedactedResponse fails when the raw secret reaches the wire.
// The dashboard renders args and the reproducer out of these exact
// responses, so redacting here is what keeps the value out of the
// browser rather than merely out of the rendered DOM.
func assertRedactedResponse(t *testing.T, surface, body string) {
	t.Helper()
	if strings.Contains(body, ctlSecretValue) {
		t.Errorf("%s served the secret arg value to the client:\n%s", surface, body)
	}
	if !strings.Contains(body, store.RedactedArgValue) {
		t.Errorf("%s carries no %s marker:\n%s", surface, store.RedactedArgValue, body)
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

func TestSecretArgs_ControllerGetRunRedactsByDefault(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRun(t, st, "run-1")
	assertRedactedResponse(t, "GET /api/v1/runs/{id}", getBody(t, srv.URL+"/api/v1/runs/run-1"))
	assertRedactedResponse(t, "GET /api/v1/runs/{id}?include=nodes",
		getBody(t, srv.URL+"/api/v1/runs/run-1?include=nodes"))
}

func TestSecretArgs_ControllerReceiptRedacts(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRun(t, st, "run-1")
	body := getBody(t, srv.URL+"/api/v1/runs/run-1/receipt")
	if strings.Contains(body, ctlSecretValue) {
		t.Errorf("receipt served the secret arg value:\n%s", body)
	}
	if !strings.Contains(body, store.RedactedArgValue) {
		t.Errorf("receipt carries no redaction marker:\n%s", body)
	}
}

func TestSecretArgs_ControllerPipelineLatestRedacts(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRun(t, st, "run-1")
	if err := st.FinishRun(context.Background(), "run-1", "success", ""); err != nil {
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

// Retry must still re-execute with the real value. It reads the stored
// row in process, so response-boundary redaction cannot reach it --
// this test is the guard that keeps it that way.
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
	src, err := st.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if src.Args["token"] != ctlSecretValue {
		t.Fatalf("source run args were mutated by a read path: %q", src.Args["token"])
	}
	if src.RetriedAs == "" {
		t.Fatal("retry did not record RetriedAs")
	}
	retried, err := st.GetRun(ctx, src.RetriedAs)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Args["token"] != ctlSecretValue {
		t.Errorf("retry run args[token] = %q, want plaintext %q -- the retry would execute with a literal %s",
			retried.Args["token"], ctlSecretValue, store.RedactedArgValue)
	}
	trig, err := st.GetTrigger(ctx, src.RetriedAs)
	if err != nil {
		t.Fatal(err)
	}
	if trig.Args["token"] != ctlSecretValue {
		t.Errorf("retry trigger args[token] = %q, want plaintext", trig.Args["token"])
	}

	// The retry's own pending row is listable before any worker picks
	// it up -- and forever if none does. It must redact for that whole
	// window, which it can only do by inheriting the source's
	// classification.
	if got := retried.SecretArgNames(); len(got) != 1 || got[0] != "token" {
		t.Fatalf("retry run classification = %v, want [token]", got)
	}
	assertRedactedResponse(t, "GET /api/v1/runs/{id} for a pending retry",
		getBody(t, srv.URL+"/api/v1/runs/"+src.RetriedAs))
}

// The same window exists for a fresh trigger's pre-allocated pending
// row, but the controller holds no pipeline schema to classify from,
// so it stays plaintext until the orchestrator upgrades the row at run
// start. Pinned so the gap is a recorded decision rather than a
// surprise; closing it needs the classification to reach the
// controller, which is a wire-protocol change.
func TestSecretArgs_ControllerPendingTriggerRowIsNotYetClassified(t *testing.T) {
	st, srv := secretArgController(t)
	err := st.CreateRun(context.Background(), store.Run{
		ID: "pending-1", Pipeline: "deploy", Status: "pending",
		Args:      map[string]string{"token": ctlSecretValue},
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := getBody(t, srv.URL+"/api/v1/runs/pending-1")
	if !strings.Contains(body, ctlSecretValue) {
		t.Errorf("pending trigger-intake rows are documented as unclassified; "+
			"if this now redacts, update CHANGELOG.md and docs/sdk.md:\n%s", body)
	}
}

// Runs written before the classification existed have no secret_args
// entry, so the controller serves them exactly as it did before.
func TestSecretArgs_ControllerGrandfathersOldRuns(t *testing.T) {
	st, srv := secretArgController(t)
	err := st.CreateRun(context.Background(), store.Run{
		ID: "old-run", Pipeline: "deploy", Status: "success",
		Args:      map[string]string{"token": ctlSecretValue},
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := getBody(t, srv.URL+"/api/v1/runs/old-run")
	if !strings.Contains(body, ctlSecretValue) {
		t.Errorf("old run without classification changed shape; want unchanged rendering:\n%s", body)
	}
}

// The run API is not only a display surface: a cluster executor
// fetches the args it is about to run with from this same endpoint.
// ?include=secret_values is how it asks, and nodes.claim -- the scope
// every node-execution endpoint already requires -- is what entitles
// it.
func TestSecretArgs_ExecutionViewIsScopeGated(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seedSecretArgRun(t, st, "run-1")

	now := time.Now().UTC()
	runnerTok, _, err := st.CreateToken("pod", store.TokenKindRunner,
		[]string{"nodes.claim", "runs.read"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken runner: %v", err)
	}
	readerTok, _, err := st.CreateToken("dashboard", store.TokenKindUser,
		[]string{"runs.read"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken reader: %v", err)
	}
	adminTok, _, err := st.CreateToken("ops", store.TokenKindUser,
		[]string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()

	get := func(t *testing.T, token, query string) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs/run-1"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d for %q", resp.StatusCode, query)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	const secretValues = "?include=" + store.IncludeSecretValues

	// An executor gets the real value, or its pod plans with "***".
	if body := get(t, runnerTok, secretValues); !strings.Contains(body, ctlSecretValue) {
		t.Errorf("nodes.claim token did not receive the execution view:\n%s", body)
	}
	if body := get(t, adminTok, secretValues); !strings.Contains(body, ctlSecretValue) {
		t.Errorf("admin token did not receive the execution view:\n%s", body)
	}

	// A display-scope token asking for it anyway stays redacted.
	assertRedactedResponse(t, "runs.read token asking for the execution view",
		get(t, readerTok, secretValues))

	// And the executor's own default read is still redacted, so the
	// opt-in is what carries the secret, not the token.
	assertRedactedResponse(t, "nodes.claim token without the include",
		get(t, runnerTok, ""))
}

// The execution view must not widen any other run-serving endpoint:
// list, attempts and pipeline-latest have no executor use, so they
// stay redacted no matter what the caller asks for.
func TestSecretArgs_ExecutionViewDoesNotWidenOtherEndpoints(t *testing.T) {
	st, srv := secretArgController(t)
	seedSecretArgRun(t, st, "run-1")
	if err := st.FinishRun(context.Background(), "run-1", "success", ""); err != nil {
		t.Fatal(err)
	}
	const q = "?include=" + store.IncludeSecretValues
	assertRedactedResponse(t, "GET /api/v1/runs"+q, getBody(t, srv.URL+"/api/v1/runs"+q))
	assertRedactedResponse(t, "GET /api/v1/runs/{id}/attempts"+q,
		getBody(t, srv.URL+"/api/v1/runs/run-1/attempts"+q))
	assertRedactedResponse(t, "GET /api/v1/pipelines/{name}/latest"+q,
		getBody(t, srv.URL+"/api/v1/pipelines/deploy/latest"+q))
	assertRedactedResponse(t, "GET /api/v1/runs/{id}/receipt"+q,
		getBody(t, srv.URL+"/api/v1/runs/run-1/receipt"+q))
}
