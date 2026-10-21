package controller

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
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

func TestGitHubInboxPersistsBeforeGitHubAndRetriesWithoutRedelivery(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	calls, unavailable := 0, true
	previewUnavailable := false
	projectConfig := ""
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
			case "GET /repos/acme/widgets/commits/main":
				body = `{"sha":"0123456789abcdef0123456789abcdef01234567"}`
			case "GET /repos/acme/widgets/contents/.sparkwing/sparkwing.yaml":
				if projectConfig != "" {
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
	if err != nil || outcome == nil || outcome.status != http.StatusInternalServerError || outcome.retryReason() == "" {
		t.Fatalf("database blip lost delivery: %+v,%v", outcome, err)
	}
	engineSQL(t, e.Store, `ALTER TABLE temporarily_unavailable_triggers RENAME TO github_app_triggers`)
	outcome, err = s.processNextGitHubDelivery(t.Context(), now.Add(10*time.Second))
	if err != nil || outcome == nil || outcome.retryReason() == "" || calls == 0 {
		t.Fatalf("unavailable GitHub = %+v, %v; calls = %d", outcome, err, calls)
	}
	var state string
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT state FROM github_app_deliveries WHERE delivery_id='delivery'`).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("delivery after provider failure = %q, %v", state, err)
	}
	unavailable = false
	// A new controller drains the same durable inbox without another webhook.
	restarted := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithGitHubApp(cfg)
	outcome, err = restarted.processNextGitHubDelivery(t.Context(), now.Add(time.Minute))
	if err != nil || outcome == nil || outcome.retryReason() != "" || outcome.status >= 400 {
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
	if err != nil || outcome == nil || outcome.status != http.StatusAccepted || outcome.retryReason() == "" || !strings.Contains(outcome.body.String(), `"dispatched"`) {
		t.Fatalf("manual run with unavailable automation = %+v, %v", outcome, err)
	}
	previewUnavailable = false
	outcome, err = restarted.processNextGitHubDelivery(t.Context(), now.Add(3*time.Minute))
	if err != nil || outcome == nil || outcome.retryReason() != "" || !strings.Contains(outcome.body.String(), `"duplicate"`) {
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
	if err != nil || outcome == nil || outcome.retryReason() == "" || !strings.Contains(outcome.body.String(), `"queued"`) {
		t.Fatalf("partial budget admission = %+v, %v", outcome, err)
	}
	s.WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
	outcome, err = restarted.processNextGitHubDelivery(t.Context(), now.Add(5*time.Minute))
	if err != nil || outcome == nil || outcome.retryReason() != "" || !strings.Contains(outcome.body.String(), `"duplicate"`) {
		t.Fatalf("partial budget retry = %+v, %v", outcome, err)
	}
	rows, err = tenant.ListRuns(t.Context(), store.RunFilter{Limit: 100})
	if err != nil || len(rows) != 6 {
		t.Fatalf("runs after partial budget retry=%d, %v; outcome=%s", len(rows), err, outcome.body.String())
	}
}
