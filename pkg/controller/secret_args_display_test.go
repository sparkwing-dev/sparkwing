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

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	ctlSecretValue  = "s3cr3t-token-value"
	ctlVisibleValue = "prod"
)

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
	if _, err := st.DB().Exec(`UPDATE runs SET invocation_json = json_set(invocation_json, '$.inputs_hash', 'sha256:offline-oracle') WHERE id = 'run-1'`); err != nil {
		t.Fatal(err)
	}
	body := getBody(t, srv.URL+"/api/v1/runs/run-1/receipt")
	if strings.Contains(body, ctlSecretValue) {
		t.Errorf("receipt served the secret arg value:\n%s", body)
	}
	if !strings.Contains(body, store.RedactedArgValue) {
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
	if _, err := st.GetRun(context.Background(), "old-writer"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun error = %v, want ErrNotFound", err)
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

	if got := retried.SecretArgNames(); len(got) != 1 || got[0] != "token" {
		t.Fatalf("retry run classification = %v, want [token]", got)
	}
	assertRedactedResponse(t, "GET /api/v1/runs/{id} for a pending retry",
		getBody(t, srv.URL+"/api/v1/runs/"+src.RetriedAs))
}

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

func TestSecretArgs_ExecutionViewIsScopeGated(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seedSecretArgRun(t, st, "run-1")

	now := time.Now().UTC()
	runnerTok, runnerRow, err := st.CreateToken("pod", store.TokenKindRunner,
		[]string{"nodes.claim", "runs.read"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken runner: %v", err)
	}
	idleRunnerTok, _, err := st.CreateToken("idle-pod", store.TokenKindRunner,
		[]string{"nodes.claim", "runs.read"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken idle runner: %v", err)
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

	ctx := context.Background()
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "only", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "only"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	if _, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "pod", TokenPrefix: runnerRow.Prefix}, "holder-1", time.Minute, nil); err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
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

	if body := get(t, runnerTok, secretValues); !strings.Contains(body, ctlSecretValue) {
		t.Errorf("the claiming runner did not receive the execution view:\n%s", body)
	}
	assertRedactedResponse(t, "nodes.claim token holding no claim on the run",
		get(t, idleRunnerTok, secretValues))
	if body := get(t, adminTok, secretValues); !strings.Contains(body, ctlSecretValue) {
		t.Errorf("admin token did not receive the execution view:\n%s", body)
	}

	assertRedactedResponse(t, "runs.read token asking for the execution view",
		get(t, readerTok, secretValues))

	assertRedactedResponse(t, "nodes.claim token without the include",
		get(t, runnerTok, ""))
}

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

func TestSecretArgs_ExecutionViewFollowsTheAuthMode(t *testing.T) {
	t.Run("auth disabled serves plaintext", func(t *testing.T) {
		st, srv := secretArgController(t)
		seedSecretArgRun(t, st, "run-1")
		body := getBody(t, srv.URL+"/api/v1/runs/run-1?include="+store.IncludeSecretValues)
		if !strings.Contains(body, ctlSecretValue) {
			t.Errorf("an unauthenticated controller redacted the execution view:\n%s", body)
		}
	})

	t.Run("auth enabled refuses an anonymous caller", func(t *testing.T) {
		st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if _, _, err := st.CreateToken("ops", store.TokenKindUser,
			[]string{"admin"}, 0, time.Now().UTC()); err != nil {
			t.Fatalf("CreateToken: %v", err)
		}
		seedSecretArgRun(t, st, "run-1")
		srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/api/v1/runs/run-1?include=" + store.IncludeSecretValues)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous execution view = %d, want 401", resp.StatusCode)
		}
		if strings.Contains(string(body), ctlSecretValue) {
			t.Errorf("anonymous execution view carried the secret arg value:\n%s", body)
		}
	})
}
