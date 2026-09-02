package controller_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	return newBoundWebhookServer(t, secret, controller.GitHubWebhookConfig{})
}

func newBoundWebhookServer(
	t *testing.T, secret string, cfg controller.GitHubWebhookConfig,
) (*httptest.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	srv := controller.New(st, nil).
		WithGitHubWebhookSecret(secret).
		WithGitHubWebhookConfig(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func postWebhook(t *testing.T, url, event string, body []byte, sig string) *http.Response {
	t.Helper()
	return postWebhookDelivery(t, url, event, "test-delivery-abc", body, sig)
}

func postWebhookDelivery(t *testing.T, url, event, delivery string, body []byte, sig string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
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

func TestWebhookGitHub_SecretUnset(t *testing.T) {
	ts, _ := newWebhookServer(t, "")
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", []byte("{}"), "sha256=deadbeef")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d want 503", resp.StatusCode)
	}
}

func TestWebhookGitHub_Ping(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"zen":"Keep it simple."}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "ping", body, signWebhook(testWebhookSecret, body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 200 (body %s)", resp.StatusCode, raw)
	}
	expectNoTrigger(t, st)
}

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
	defer func() { _ = resp.Body.Close() }()
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
	if tr.Repo != "acme/sample-app" || tr.GithubOwner != "acme" || tr.GithubRepo != "sample-app" {
		t.Errorf("repository provenance = %q %q/%q", tr.Repo, tr.GithubOwner, tr.GithubRepo)
	}
	if tr.TriggerEnv["GITHUB_REPOSITORY"] != "acme/sample-app" {
		t.Errorf("env[GITHUB_REPOSITORY]=%q", tr.TriggerEnv["GITHUB_REPOSITORY"])
	}
	if tr.TriggerEnv["GITHUB_DELIVERY"] != "test-delivery-abc" {
		t.Errorf("env[GITHUB_DELIVERY]=%q", tr.TriggerEnv["GITHUB_DELIVERY"])
	}
}

func TestWebhookGitHub_BadSignature(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"ref":"refs/heads/main","after":"x","repository":{"full_name":"x/y"}}`)
	bad := signWebhook("wrong-secret", body)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, bad)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", resp.StatusCode)
	}
	expectNoTrigger(t, st)
}

func TestWebhookGitHub_MissingSignature(t *testing.T) {
	ts, _ := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"ref":"refs/heads/main"}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", resp.StatusCode)
	}
}

func TestWebhookGitHub_TagPushIgnored(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{
		"ref": "refs/tags/v1.2.3",
		"after": "abc",
		"repository": {"full_name": "x/y"}
	}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, signWebhook(testWebhookSecret, body))
	defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status=%d want 202", resp.StatusCode)
	}
	expectNoTrigger(t, st)
}

func TestWebhookGitHub_UnknownEventIgnored(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"action":"opened"}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "issues", body, signWebhook(testWebhookSecret, body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status=%d want 202", resp.StatusCode)
	}
	expectNoTrigger(t, st)
}

func prWebhookBody(action string, number int) []byte {
	return []byte(fmt.Sprintf(`{
		"action": %q,
		"number": %d,
		"pull_request": {
			"head": {"ref": "feature/login", "sha": "1111111111111111111111111111111111111111"},
			"base": {"ref": "main", "sha": "2222222222222222222222222222222222222222"},
			"user": {"login": "bob"}
		},
		"repository": {"full_name": "acme/sample-app"}
	}`, action, number))
}

func TestWebhookGitHub_PullRequestDispatches(t *testing.T) {
	for _, action := range []string{"opened", "synchronize", "reopened"} {
		t.Run(action, func(t *testing.T) {
			ts, st := newWebhookServer(t, testWebhookSecret)
			body := prWebhookBody(action, 42)
			resp := postWebhook(t, ts.URL+"/webhooks/github/pr-gate", "pull_request", body, signWebhook(testWebhookSecret, body))
			defer func() { _ = resp.Body.Close() }()
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
			if decoded.Status != "dispatched" {
				t.Fatalf("status=%q want dispatched", decoded.Status)
			}
			tr, err := st.GetTrigger(context.Background(), decoded.RunID)
			if err != nil {
				t.Fatalf("GetTrigger: %v", err)
			}
			if tr.Pipeline != "pr-gate" {
				t.Errorf("pipeline=%q want pr-gate", tr.Pipeline)
			}
			if tr.GitBranch != "feature/login" {
				t.Errorf("branch=%q want feature/login (PR head)", tr.GitBranch)
			}
			if tr.GitSHA != "1111111111111111111111111111111111111111" {
				t.Errorf("sha=%q want PR head sha", tr.GitSHA)
			}
			if tr.Repo != "acme/sample-app" || tr.GithubOwner != "acme" || tr.GithubRepo != "sample-app" {
				t.Errorf("repository provenance = %q %q/%q", tr.Repo, tr.GithubOwner, tr.GithubRepo)
			}
			if tr.TriggerUser != "bob" {
				t.Errorf("user=%q want bob", tr.TriggerUser)
			}
			want := map[string]string{
				"GITHUB_EVENT_NAME": "pull_request",
				"GITHUB_PR_NUMBER":  "42",
				"GITHUB_PR_ACTION":  action,
				"GITHUB_BASE_REF":   "main",
				"GITHUB_HEAD_REF":   "feature/login",
				"GITHUB_HEAD_SHA":   "1111111111111111111111111111111111111111",
				"GITHUB_BASE_SHA":   "2222222222222222222222222222222222222222",
				"GITHUB_REPOSITORY": "acme/sample-app",
			}
			for k, v := range want {
				if tr.TriggerEnv[k] != v {
					t.Errorf("env[%s]=%q want %q", k, tr.TriggerEnv[k], v)
				}
			}
		})
	}
}

func TestWebhookGitHub_PullRequestActionIgnored(t *testing.T) {
	for _, action := range []string{"closed", "labeled", "edited", "assigned"} {
		t.Run(action, func(t *testing.T) {
			ts, st := newWebhookServer(t, testWebhookSecret)
			body := prWebhookBody(action, 7)
			resp := postWebhook(t, ts.URL+"/webhooks/github/pr-gate", "pull_request", body, signWebhook(testWebhookSecret, body))
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusAccepted {
				t.Errorf("status=%d want 202", resp.StatusCode)
			}
			var decoded map[string]string
			_ = json.NewDecoder(resp.Body).Decode(&decoded)
			if decoded["status"] != "ignored" {
				t.Errorf("status=%q want ignored", decoded["status"])
			}
			expectNoTrigger(t, st)
		})
	}
}

func TestWebhookGitHub_BodyTooLarge(t *testing.T) {
	ts, _ := newWebhookServer(t, testWebhookSecret)
	body := []byte(`{"filler":"` + strings.Repeat("x", 2<<20) + `"}`)
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, signWebhook(testWebhookSecret, body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status=%d want 413", resp.StatusCode)
	}
}

func pushBodyFor(repo string) []byte {
	return []byte(fmt.Sprintf(`{
		"ref": "refs/heads/main",
		"before": "0000000000000000000000000000000000000000",
		"after":  "abc123def456abc123def456abc123def456abcd",
		"repository": {"full_name": %q},
		"pusher": {"name": "alice", "email": "alice@example.com"}
	}`, repo))
}

func TestWebhookGitHub_RepositoryBinding(t *testing.T) {
	cfg := controller.GitHubWebhookConfig{
		Pipelines: map[string]controller.GitHubWebhookBinding{
			"sample-app-build": {Repos: []string{"acme/sample-app"}},
			"deny-all":         {Repos: []string{}},
		},
	}
	for _, tc := range []struct {
		name     string
		pipeline string
		repo     string
		want     int
	}{
		{"bound repository dispatches", "sample-app-build", "acme/sample-app", http.StatusAccepted},
		{"binding ignores slug case", "sample-app-build", "ACME/Sample-App", http.StatusAccepted},
		{"stranger repository refused", "sample-app-build", "attacker/evil", http.StatusNotFound},
		{"missing repository refused", "sample-app-build", "", http.StatusNotFound},
		{"unbound pipeline is unchanged", "legacy-build", "attacker/evil", http.StatusAccepted},
		{"non-ascii fold refused", "sample-app-build", "acme/\u017fample-app", http.StatusNotFound},
		{"non-ascii fold refused at an unbound pipeline too", "legacy-build", "acme/\u017fample-app", http.StatusNotFound},
		{"an empty repos list refuses every repository", "deny-all", "acme/sample-app", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, st := newBoundWebhookServer(t, testWebhookSecret, cfg)
			body := pushBodyFor(tc.repo)
			resp := postWebhook(t, ts.URL+"/webhooks/github/"+tc.pipeline, "push", body,
				signWebhook(testWebhookSecret, body))
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status=%d want %d (body %s)", resp.StatusCode, tc.want, raw)
			}
			if tc.want != http.StatusAccepted {
				expectNoTrigger(t, st)
			}
		})
	}
}

func TestWebhookGitHub_ScopedSecrets(t *testing.T) {
	cfg := controller.GitHubWebhookConfig{
		Pipelines: map[string]controller.GitHubWebhookBinding{
			"pipeline-scoped": {Secret: "pipeline-secret"},
		},
		RepoSecrets: map[string]string{"acme/sample-app": "repo-secret"},
	}
	for _, tc := range []struct {
		name     string
		pipeline string
		repo     string
		signWith string
		want     int
	}{
		{"pipeline secret signs its own pipeline", "pipeline-scoped", "acme/other", "pipeline-secret", http.StatusAccepted},
		{"shared secret cannot reach a scoped pipeline", "pipeline-scoped", "acme/other", testWebhookSecret, http.StatusUnauthorized},
		{"pipeline secret outranks the repository secret", "pipeline-scoped", "acme/sample-app", "repo-secret", http.StatusUnauthorized},
		{"repository secret signs its own repository", "demo", "acme/sample-app", "repo-secret", http.StatusAccepted},
		{"shared secret cannot forge a scoped repository", "demo", "acme/sample-app", testWebhookSecret, http.StatusUnauthorized},
		{"repository secret cannot forge a peer", "demo", "acme/other", "repo-secret", http.StatusUnauthorized},
		{"shared secret still serves unscoped repositories", "demo", "acme/other", testWebhookSecret, http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, st := newBoundWebhookServer(t, testWebhookSecret, cfg)
			body := pushBodyFor(tc.repo)
			resp := postWebhook(t, ts.URL+"/webhooks/github/"+tc.pipeline, "push", body,
				signWebhook(tc.signWith, body))
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status=%d want %d (body %s)", resp.StatusCode, tc.want, raw)
			}
			if tc.want != http.StatusAccepted {
				expectNoTrigger(t, st)
			}
		})
	}
}

func TestWebhookGitHub_NoSecretMatchesTheSignatureRefusal(t *testing.T) {
	cfg := controller.GitHubWebhookConfig{
		RepoSecrets: map[string]string{"acme/sample-app": "repo-secret"},
	}
	ts, _ := newBoundWebhookServer(t, "", cfg)
	for _, tc := range []struct {
		name string
		repo string
	}{
		{"a repository with a secret answers the signature refusal", "acme/sample-app"},
		{"a repository without one answers the same", "acme/other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := pushBodyFor(tc.repo)
			resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body, "sha256=deadbeef")
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status=%d want 401", resp.StatusCode)
			}
		})
	}
}

func TestWebhookGitHub_NonASCIIFoldCannotSplitSecretFromBinding(t *testing.T) {
	cfg := controller.GitHubWebhookConfig{
		Pipelines: map[string]controller.GitHubWebhookBinding{
			"demo": {Repos: []string{"acme/service"}},
		},
		RepoSecrets: map[string]string{"acme/service": "per-repo-secret"},
	}
	ts, st := newBoundWebhookServer(t, testWebhookSecret, cfg)

	body := pushBodyFor("acme/\u017fervice")
	resp := postWebhook(t, ts.URL+"/webhooks/github/demo", "push", body,
		signWebhook(testWebhookSecret, body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 404 (body %s)", resp.StatusCode, raw)
	}
	expectNoTrigger(t, st)
}

func TestWebhookGitHub_ReplayedDeliveryConflicts(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := pushBodyFor("acme/sample-app")
	sig := signWebhook(testWebhookSecret, body)

	first := postWebhookDelivery(t, ts.URL+"/webhooks/github/sample-app-build", "push", "delivery-7", body, sig)
	defer func() { _ = first.Body.Close() }()
	if first.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(first.Body)
		t.Fatalf("first status=%d want 202 (body %s)", first.StatusCode, raw)
	}

	replay := postWebhookDelivery(t, ts.URL+"/webhooks/github/sample-app-build", "push", "delivery-7", body, sig)
	defer func() { _ = replay.Body.Close() }()
	if replay.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(replay.Body)
		t.Fatalf("replay status=%d want 409 (body %s)", replay.StatusCode, raw)
	}

	crossPipeline := postWebhookDelivery(t, ts.URL+"/webhooks/github/other-build", "push", "delivery-7", body, sig)
	defer func() { _ = crossPipeline.Body.Close() }()
	if crossPipeline.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(crossPipeline.Body)
		t.Fatalf("cross-pipeline replay status=%d want 409 (body %s)", crossPipeline.StatusCode, raw)
	}

	relabeled := postWebhookDelivery(t, ts.URL+"/webhooks/github/sample-app-build", "push", "delivery-8", body, sig)
	defer func() { _ = relabeled.Body.Close() }()
	if relabeled.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(relabeled.Body)
		t.Fatalf("relabeled replay status=%d want 409 (body %s)", relabeled.StatusCode, raw)
	}

	fresh := pushBodyFor("acme/sample-app")
	fresh = append(fresh[:len(fresh)-1], []byte(`, "deleted": false}`)...)
	freshResp := postWebhookDelivery(t, ts.URL+"/webhooks/github/sample-app-build", "push", "delivery-9",
		fresh, signWebhook(testWebhookSecret, fresh))
	defer func() { _ = freshResp.Body.Close() }()
	if freshResp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(freshResp.Body)
		t.Fatalf("fresh delivery status=%d want 202 (body %s)", freshResp.StatusCode, raw)
	}

	var pending int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM triggers`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Errorf("triggers = %d, want 2 (one per distinct signed body)", pending)
	}
}

func TestWebhookGitHub_DuplicateNamesTheRunItAlreadyMade(t *testing.T) {
	ts, _ := newWebhookServer(t, testWebhookSecret)
	body := pushBodyFor("acme/sample-app")
	sig := signWebhook(testWebhookSecret, body)

	first := postWebhookDelivery(t, ts.URL+"/webhooks/github/sample-app-build", "push", "delivery-7", body, sig)
	defer func() { _ = first.Body.Close() }()
	var accepted struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(first.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if accepted.RunID == "" {
		t.Fatal("first delivery returned no run_id")
	}

	redelivery := postWebhookDelivery(t, ts.URL+"/webhooks/github/sample-app-build", "push", "delivery-7", body, sig)
	defer func() { _ = redelivery.Body.Close() }()
	if redelivery.StatusCode != http.StatusConflict {
		t.Fatalf("redelivery status=%d want 409", redelivery.StatusCode)
	}
	var duplicate struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(redelivery.Body).Decode(&duplicate); err != nil {
		t.Fatalf("decode redelivery response: %v", err)
	}
	if duplicate.RunID != accepted.RunID {
		t.Errorf("duplicate run_id = %q, want the accepted run %q", duplicate.RunID, accepted.RunID)
	}
	if duplicate.Status != "duplicate" {
		t.Errorf("duplicate status = %q, want duplicate", duplicate.Status)
	}
}

func TestWebhookGitHub_MissingDeliveryHeader(t *testing.T) {
	ts, st := newWebhookServer(t, testWebhookSecret)
	body := pushBodyFor("acme/sample-app")
	resp := postWebhookDelivery(t, ts.URL+"/webhooks/github/demo", "push", "", body,
		signWebhook(testWebhookSecret, body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.StatusCode)
	}
	expectNoTrigger(t, st)
}

func TestParseGitHubWebhookConfig_MalformedDocuments(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"a second document after the first", `{"pipelines":{"a":{"repos":["x/y"]}}} {"repo_secrets":{"x/y":"s"}}`},
		{"trailing text that is not json", `{"pipelines":{"a":{"repos":["x/y"]}}} this is not json at all`},
		{"an unknown top-level field", `{"pipelines":{},"repo_allowlist":["x/y"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := controller.ParseGitHubWebhookConfig(tc.raw); err == nil {
				t.Fatal("ParseGitHubWebhookConfig accepted a malformed document")
			}
		})
	}
}

func TestParseGitHubWebhookConfig_DuplicateReposKeyDeniesEveryRepository(t *testing.T) {
	cfg, err := controller.ParseGitHubWebhookConfig(`{"pipelines":{"a":{"repos":["x/y"],"repos":[]}}}`)
	if err != nil {
		t.Fatalf("ParseGitHubWebhookConfig: %v", err)
	}
	binding := cfg.Pipelines["a"]
	if binding.Repos == nil || len(binding.Repos) != 0 {
		t.Fatalf("Repos = %#v, want a present-but-empty list", binding.Repos)
	}
	if got := cfg.BindingCounts(); got.DenyAll != 1 {
		t.Errorf("BindingCounts().DenyAll = %d, want 1", got.DenyAll)
	}
}

func TestParseGitHubWebhookConfig_BindingCounts(t *testing.T) {
	cfg, err := controller.ParseGitHubWebhookConfig(
		`{"pipelines":{"a":{"repos":["x/y","x/z"]},"b":{"repos":[]},"c":{"secret":"s"}},` +
			`"repo_secrets":{"x/y":"s"}}`)
	if err != nil {
		t.Fatalf("ParseGitHubWebhookConfig: %v", err)
	}
	want := controller.GitHubWebhookBindingCounts{Pipelines: 3, Repos: 2, DenyAll: 1, RepoSecrets: 1}
	if got := cfg.BindingCounts(); got != want {
		t.Errorf("BindingCounts() = %+v, want %+v", got, want)
	}
}
