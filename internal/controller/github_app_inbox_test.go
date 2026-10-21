package controller

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestGitHubInboxPersistsFailuresUntilManualProcessing(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	calls, unavailable := 0, true
	previewUnavailable := false
	projectConfig := ""
	configRef := ""
	cfg := githubapp.Config{AppID: 1, PrivateKey: key, WebhookSecret: "hook", HTTP: &http.Client{Transport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		code, body := http.StatusOK, ""
		if unavailable {
			code = http.StatusServiceUnavailable
		} else {
			switch r.Method + " " + r.URL.Path {
			case "GET /repos/acme/widgets/installation":
				body = `{"id":7,"app_id":1}`
			case "POST /app/installations/7/access_tokens":
				body = fmt.Sprintf(`{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
			case "GET /repos/acme/widgets":
				if previewUnavailable {
					code = http.StatusServiceUnavailable
				} else {
					body = `{"id":701,"default_branch":"main"}`
				}
			case "GET /repos/acme/widgets/git/ref/heads/main":
				body = `{"ref":"refs/heads/main","object":{"type":"commit","sha":"0123456789abcdef0123456789abcdef01234567"}}`
			case "GET /repos/acme/widgets/contents/.sparkwing/sparkwing.yaml":
				if projectConfig != "" && (configRef == "" || r.URL.Query().Get("ref") == configRef) {
					body = projectConfig
				} else {
					code = http.StatusNotFound
				}
			default:
				t.Errorf("unexpected GitHub call: %s %s", r.Method, r.URL)
				code = http.StatusNotFound
			}
		}
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	e, tenant := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithGitHubApp(cfg) })
	s := e.Server
	engineSQL(t, e.Store, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',42,'acme','Organization',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO github_app_triggers(team,repository_id,pipeline,repository,installation_id,on_push,created_at,repo_key) VALUES ('a',701,'build','acme/widgets',7,1,1,'gh:701')`)
	now := time.Now()
	processAgain := func(id string) {
		t.Helper()
		if next, err := s.processNextGitHubDelivery(t.Context(), now.Add(24*time.Hour)); err != nil || next != nil {
			t.Fatalf("failed delivery retried automatically: %+v, %v", next, err)
		}
		engineSQL(t, e.Store, `UPDATE github_app_deliveries SET state='pending' WHERE delivery_id=? AND state='failed'`, id)
	}
	body := fmt.Sprintf(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","installation":{"id":7},"repository":{"id":701,"full_name":"acme/widgets","pushed_at":%d}}`, now.Unix())
	request := httptest.NewRequest(http.MethodPost, "/webhooks/github-app", strings.NewReader(body))
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("X-GitHub-Delivery", "delivery")
	mac := hmac.New(sha256.New, []byte("hook"))
	_, _ = mac.Write([]byte(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"queued"`) || calls != 0 {
		t.Fatalf("ingress = %d %s; GitHub calls = %d", response.Code, response.Body, calls)
	}
	engineSQL(t, e.Store, `ALTER TABLE github_app_triggers RENAME TO temporarily_unavailable_triggers`)
	outcome, err := s.processNextGitHubDelivery(t.Context(), now.Add(time.Second))
	if err != nil || outcome == nil || outcome.status != http.StatusInternalServerError || outcome.failureReason() == "" {
		t.Fatalf("database blip lost delivery: %+v,%v", outcome, err)
	}
	engineSQL(t, e.Store, `ALTER TABLE temporarily_unavailable_triggers RENAME TO github_app_triggers`)
	processAgain("delivery")
	outcome, err = s.processNextGitHubDelivery(t.Context(), now.Add(10*time.Second))
	if err != nil || outcome == nil || outcome.failureReason() == "" || calls == 0 {
		t.Fatalf("unavailable GitHub = %+v, %v; calls = %d", outcome, err, calls)
	}
	var state string
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT state FROM github_app_deliveries WHERE delivery_id='delivery'`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("delivery after provider failure = %q, %v", state, err)
	}
	unavailable = false
	// A controller restart preserves the failed delivery until manual processing.
	restarted := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithGitHubApp(cfg)
	processAgain("delivery")
	outcome, err = restarted.processNextGitHubDelivery(t.Context(), now.Add(time.Minute))
	if err != nil || outcome == nil || outcome.failureReason() != "" || outcome.status >= 400 {
		t.Fatalf("retry = %+v, %v", outcome, err)
	}
	rows, err := tenant.ListRuns(t.Context(), store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, run := range rows {
		if run.Pipeline == "build" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("recovered build runs = %d; outcome = %s", found, outcome.body.String())
	}
	if next, err := restarted.processNextGitHubDelivery(t.Context(), now.Add(time.Hour)); err != nil || next != nil {
		t.Fatalf("completed delivery replayed: %+v, %v", next, err)
	}
	restarted = s
	engineSQL(t, e.Store, `INSERT INTO github_app_automation(team,repository_id,repository,installation_id,enabled_by,enabled_at,repo_key) VALUES ('a',701,'acme/widgets',7,'admin',1,'gh:701')`)
	projectConfig = "pipelines:\n  - name: build\n    entrypoint: Build\n    on:\n      push: {}\n  - name: automatic\n    entrypoint: Automatic\n    on:\n      push: {}\n"
	secondBody := strings.Replace(body, "0123456789abcdef0123456789abcdef01234567", strings.Repeat("a", 40), 1)
	previewUnavailable = true
	if err := e.Store.AsOperator().EnqueueGitHubDelivery(t.Context(), "partial-discovery", "push", []byte(secondBody), now); err != nil {
		t.Fatal(err)
	}
	outcome, err = restarted.processNextGitHubDelivery(t.Context(), now.Add(2*time.Minute))
	if err != nil || outcome == nil || outcome.status != http.StatusAccepted || outcome.failureReason() == "" || !strings.Contains(outcome.body.String(), `"dispatched"`) {
		t.Fatalf("manual run with unavailable automation = %+v, %v", outcome, err)
	}
	previewUnavailable = false
	processAgain("partial-discovery")
	outcome, err = restarted.processNextGitHubDelivery(t.Context(), now.Add(3*time.Minute))
	if err != nil || outcome == nil || outcome.failureReason() != "" || !strings.Contains(outcome.body.String(), `"duplicate"`) {
		t.Fatalf("automation recovery = %+v, %v", outcome, err)
	}
	rows, err = tenant.ListRuns(t.Context(), store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	build, automatic := 0, 0
	for _, run := range rows {
		if run.Pipeline == "build" {
			build++
		}
		if run.Pipeline == "automatic" {
			automatic++
		}
	}
	if build != 2 || automatic != 1 {
		t.Fatalf("partial discovery recovery build=%d automatic=%d; outcome=%s", build, automatic, outcome.body.String())
	}

	thirdBody := strings.Replace(body, "0123456789abcdef0123456789abcdef01234567", strings.Repeat("b", 40), 1)
	s.WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
	if err := e.Store.AsOperator().EnqueueGitHubDelivery(t.Context(), "partial-admission", "push", []byte(thirdBody), now); err != nil {
		t.Fatal(err)
	}
	outcome, err = restarted.processNextGitHubDelivery(t.Context(), now.Add(4*time.Minute))
	if err != nil || outcome == nil || outcome.failureReason() == "" || !strings.Contains(outcome.body.String(), `"failed"`) {
		t.Fatalf("partial budget admission = %+v, %v", outcome, err)
	}
	s.WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
	processAgain("partial-admission")
	outcome, err = restarted.processNextGitHubDelivery(t.Context(), now.Add(5*time.Minute))
	if err != nil || outcome == nil || outcome.failureReason() != "" || !strings.Contains(outcome.body.String(), `"duplicate"`) {
		t.Fatalf("partial budget retry = %+v, %v", outcome, err)
	}
	rows, err = tenant.ListRuns(t.Context(), store.RunFilter{Limit: 100})
	if err != nil || len(rows) != 6 {
		t.Fatalf("runs after partial budget retry=%d, %v; outcome=%s", len(rows), err, outcome.body.String())
	}
	s.WithFloodPolicy(FloodPolicy{})
	configRef = "0123456789abcdef0123456789abcdef01234567"
	for _, test := range []struct {
		id, sha, config string
	}{
		{"invalid-discovery", strings.Repeat("c", 40), "pipelines: ["},
		{"unsupported-discovery", strings.Repeat("d", 40), "pipelines:\n  - name: build\n    entrypoint: Build\n    on:\n      push:\n        paths: ['src/*']\n"},
	} {
		t.Run(test.id, func(t *testing.T) {
			projectConfig = test.config
			featureBody := strings.Replace(body, configRef, test.sha, 1)
			featureBody = strings.Replace(featureBody, "refs/heads/main", "refs/heads/feature", 1)
			if err := e.Store.AsOperator().EnqueueGitHubDelivery(t.Context(), test.id, "push", []byte(featureBody), now); err != nil {
				t.Fatal(err)
			}
			outcome, err := s.processNextGitHubDelivery(t.Context(), now.Add(6*time.Minute))
			if err != nil || outcome == nil || outcome.status != http.StatusAccepted || outcome.failureReason() == "" {
				t.Fatalf("partial permanent discovery failure = %+v, %v", outcome, err)
			}
			var response githubAppWebhookResp
			if err := json.Unmarshal(outcome.body.Bytes(), &response); err != nil || len(response.Runs) != 1 || response.Runs[0].Status != "dispatched" || response.Runs[0].RunID == "" {
				t.Fatalf("manual subscription did not dispatch: %s, %v", outcome.body.String(), err)
			}
			var state, reason string
			var retained []byte
			if err := teststore.SQL(t, e.Store).QueryRow(`SELECT state,last_error,body FROM github_app_deliveries WHERE delivery_id=?`, test.id).Scan(&state, &reason, &retained); err != nil || state != "failed" || reason != outcome.failureReason() || string(retained) != featureBody {
				t.Fatalf("partial failure lost payload or reason: state=%q reason=%q body=%q err=%v", state, reason, retained, err)
			}
			if next, err := s.processNextGitHubDelivery(t.Context(), now.Add(24*time.Hour)); err != nil || next != nil {
				t.Fatalf("partial permanent discovery failure retried: %+v, %v", next, err)
			}
		})
	}
}

func TestGitHubInboxLifecycleCrashBeforeCompletionKeepsNewerState(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) {
		s.WithGitHubApp(githubapp.Config{AppID: 1, PrivateKey: key, WebhookSecret: "hook"})
	})
	s, ctx, now := e.Server, t.Context(), time.Now()
	engineSQL(t, e.Store, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',42,'acme','Organization',1,1)`)
	op := e.Store.AsOperator()
	if err := op.EnqueueGitHubDelivery(ctx, "suspend", "installation", []byte(`{"action":"suspend","installation":{"id":7,"app_id":1}}`), now); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `CREATE TRIGGER fail_delivery_completion BEFORE UPDATE ON github_app_deliveries WHEN NEW.state='done' BEGIN SELECT RAISE(ABORT,'crash before completion'); END`)
	if outcome, err := s.processNextGitHubDelivery(ctx, now); err == nil || outcome == nil || outcome.failureReason() != "" {
		t.Fatalf("completion failure: %+v, %v", outcome, err)
	}
	got, err := op.GitHubAppInstallationTeam(ctx, 7)
	if err != nil || !got.Suspended {
		t.Fatalf("mutation did not commit before completion failure: %+v, %v", got, err)
	}
	engineSQL(t, e.Store, `DROP TRIGGER fail_delivery_completion`)
	if err := op.EnqueueGitHubDelivery(ctx, "unsuspend", "installation", []byte(`{"action":"unsuspend","installation":{"id":7,"app_id":1}}`), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if outcome, err := s.processNextGitHubDelivery(ctx, now.Add(time.Second)); err != nil || outcome == nil || outcome.failureReason() != "" {
		t.Fatalf("newer delivery: %+v, %v", outcome, err)
	}
	restarted := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithGitHubApp(githubapp.Config{AppID: 1, PrivateKey: key, WebhookSecret: "hook"})
	if outcome, err := restarted.processNextGitHubDelivery(ctx, now.Add(3*time.Minute)); err != nil || outcome != nil {
		t.Fatalf("expired delivery retried automatically: %+v, %v", outcome, err)
	}
	engineSQL(t, e.Store, `UPDATE github_app_deliveries SET state='pending' WHERE delivery_id='suspend' AND state='failed'`)
	if outcome, err := restarted.processNextGitHubDelivery(ctx, now.Add(4*time.Minute)); err != nil || outcome == nil || outcome.failureReason() != "" {
		t.Fatalf("manual processing: %+v, %v", outcome, err)
	}
	got, err = op.GitHubAppInstallationTeam(ctx, 7)
	if err != nil || got.Suspended {
		t.Fatalf("replayed applied suspend overwrote newer unsuspend: %+v, %v", got, err)
	}
}

func TestGitHubInboxPermanentFailureRetainsReasonWithoutRetry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) {
		s.WithGitHubApp(githubapp.Config{AppID: 1, PrivateKey: key, WebhookSecret: "hook"})
	})
	now := time.Now()
	if err := e.Store.AsOperator().EnqueueGitHubDelivery(t.Context(), "malformed", "push", []byte(`{`), now); err != nil {
		t.Fatal(err)
	}
	outcome, err := e.Server.processNextGitHubDelivery(t.Context(), now)
	if err != nil || outcome == nil || outcome.status != http.StatusBadRequest || outcome.failureReason() == "" {
		t.Fatalf("permanent failure: %+v, %v", outcome, err)
	}
	var state, reason string
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT state,last_error FROM github_app_deliveries WHERE delivery_id='malformed'`).Scan(&state, &reason); err != nil || state != "failed" || reason != outcome.failureReason() {
		t.Fatalf("permanent failure was not retained: state=%q reason=%q err=%v", state, reason, err)
	}
	if next, err := e.Server.processNextGitHubDelivery(t.Context(), now.Add(24*time.Hour)); err != nil || next != nil {
		t.Fatalf("permanent failure retried: %+v, %v", next, err)
	}
}
