package controller_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// expectNoTrigger drains the pending queue and fails if any trigger
// was enqueued. Uses ClaimNextTrigger because the store doesn't expose
// a list-all surface; ErrNotFound is the empty-queue signal.
func expectNoTrigger(t *testing.T, st *store.Store) {
	t.Helper()
	tr, err := st.ClaimNextTrigger(context.Background(), time.Minute)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	if tr != nil {
		t.Errorf("unexpected trigger enqueued: pipeline=%q id=%s", tr.Pipeline, tr.ID)
	}
}

const testWebhookSecret = "it's-a-secret-to-everybody"

func signWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newWebhookServer(t *testing.T, secret string) (*httptest.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	srv := controller.New(st, nil).WithGitHubWebhookSecret(secret)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func postWebhook(t *testing.T, url string, event string, body []byte, sig string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "test-delivery-abc")
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestWebhookGitHub_SecretUnset returns 503 rather than silently
// succeeding so misconfig is loud.
func TestWebhookGitHub_SecretUnset(t *testing.T) {
	ts, _ := newWebhookServer(t, "")
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", []byte("{}"), "sha256=deadbeef")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d want 503", resp.StatusCode)
	}
}

// TestWebhookGitHub_Ping is the request GitHub fires when a webhook is
// first created. Responds 200 without touching the store.
func TestWebhookGitHub_Ping(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"zen":"Keep it simple."}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "ping", body, signWebhook(testWebhookSecret, body))
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 200 (body %s)", resp.StatusCode, raw)
	}
	expectNoTrigger(t, st)
}

// TestWebhookGitHub_PushEnqueuesTrigger is the happy path: a valid
// push payload with correct signature lands a trigger row for the
// path-named pipeline.
func TestWebhookGitHub_PushEnqueuesTrigger(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{
		"ref": "refs/heads/main",
		"before": "0000000000000000000000000000000000000000",
		"after":  "abc123def456abc123def456abc123def456abcd",
		"repository": {"full_name": "acme/sample-app"},
		"pusher": {"name": "alice", "email": "alice@example.com"},
		"head_commit": {"id": "abc123", "message": "feat: ship it"}
	}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/sample-app-build", "push", body, signWebhook(testWebhookSecret, body))
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body %s)", resp.StatusCode, raw)
	}

	var decoded struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.RunID == "" {
		t.Error("run_id empty")
	}
	if decoded.Status != "dispatched" {
		t.Errorf("status=%q want dispatched", decoded.Status)
	}

	tr, err := st.GetTrigger(context.Background(), decoded.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if tr.Pipeline != "sample-app-build" {
		t.Errorf("pipeline=%q want sample-app-build", tr.Pipeline)
	}
	if tr.TriggerSource != "github" {
		t.Errorf("source=%q want github", tr.TriggerSource)
	}
	if tr.TriggerUser != "alice" {
		t.Errorf("user=%q want alice", tr.TriggerUser)
	}
	if tr.GitBranch != "main" {
		t.Errorf("branch=%q want main", tr.GitBranch)
	}
	if tr.GitSHA != "abc123def456abc123def456abc123def456abcd" {
		t.Errorf("sha=%q", tr.GitSHA)
	}
	if tr.TriggerEnv["GITHUB_REPOSITORY"] != "acme/sample-app" {
		t.Errorf("env[GITHUB_REPOSITORY]=%q", tr.TriggerEnv["GITHUB_REPOSITORY"])
	}
	if tr.TriggerEnv["GITHUB_DELIVERY"] != "test-delivery-abc" {
		t.Errorf("env[GITHUB_DELIVERY]=%q", tr.TriggerEnv["GITHUB_DELIVERY"])
	}
}

// TestWebhookGitHub_BadSignature rejects bodies whose HMAC doesn't
// match. Critical: an attacker knowing the handler exists must not be
// able to inject triggers.
func TestWebhookGitHub_BadSignature(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"ref":"refs/heads/main","after":"x","repository":{"full_name":"x/y"}}`)
	// Sign with a different secret.
	bad := signWebhook("wrong-secret", body)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, bad)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", resp.StatusCode)
	}
	expectNoTrigger(t, st)
}

// TestWebhookGitHub_MissingSignature rejects unsigned requests even if
// the body would otherwise parse. Belt-and-braces against operator
// confusion about whether HMAC is optional.
func TestWebhookGitHub_MissingSignature(t *testing.T) {
	ts, _ := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"ref":"refs/heads/main"}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", resp.StatusCode)
	}
}

// TestWebhookGitHub_TagPushIgnored proves tag-push events are ack'd
// but not dispatched. v1 policy: only branch pushes trigger runs.
func TestWebhookGitHub_TagPushIgnored(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{
		"ref": "refs/tags/v1.2.3",
		"after": "abc",
		"repository": {"full_name": "x/y"}
	}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, signWebhook(testWebhookSecret, body))
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status=%d want 202", resp.StatusCode)
	}
	var decoded map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	if decoded["status"] != "ignored" {
		t.Errorf("status=%q want ignored", decoded["status"])
	}
	expectNoTrigger(t, st)
}

// TestWebhookGitHub_BranchDeleteIgnored covers the git-push-with-delete
// case (push --delete). GitHub sends deleted=true; we ack and move on.
func TestWebhookGitHub_BranchDeleteIgnored(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{
		"ref": "refs/heads/feature",
		"before": "abc",
		"after": "0000000000000000000000000000000000000000",
		"deleted": true,
		"repository": {"full_name": "x/y"}
	}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, signWebhook(testWebhookSecret, body))
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status=%d want 202", resp.StatusCode)
	}
	expectNoTrigger(t, st)
}

// TestWebhookGitHub_UnknownEventIgnored acks pull_request and other
// events without dispatching, so GitHub sees a green delivery.
func TestWebhookGitHub_UnknownEventIgnored(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"action":"opened"}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "pull_request", body, signWebhook(testWebhookSecret, body))
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status=%d want 202", resp.StatusCode)
	}
	expectNoTrigger(t, st)
}

// TestWebhookGitHub_BodyTooLarge guards against memory blow-up from a
// malicious or buggy payload.
func TestWebhookGitHub_BodyTooLarge(t *testing.T) {
	ts, _ := newWebhookServer(t, testWebhookSecret)
	// 2 MiB; limit is 1 MiB.
	body := []byte(`{"filler":"` + strings.Repeat("x", 2<<20) + `"}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, signWebhook(testWebhookSecret, body))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status=%d want 413", resp.StatusCode)
	}
}
