package controller_test

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
)

type automationTransport func(*http.Request) (*http.Response, error)

func (f automationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubAppAutomationRevokedDuringDiscoveryStartsNoRun(t *testing.T) {
	for _, scenario := range []string{"revoke", "revoke and reenable", "suspend", "unbind"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAppFixture(t)
			owner := f.ghUser(501, "olga")
			f.connect(owner, 501, 7, acmeAdmin)
			f.app.SetCommit("acme/widgets", "main", headSHA)
			f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig))
			enableAutomation(t, f, owner)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var hold atomic.Bool
			hold.Store(true)
			cfg := f.app.Config()
			cfg.HTTP = &http.Client{Transport: automationTransport(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/contents/") && hold.Swap(false) {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return http.DefaultTransport.RoundTrip(r)
			})}
			f.srv.WithGitHubApp(cfg)
			result := make(chan map[string]any, 1)
			go func() {
				_, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", strings.Repeat("d", 40)), "")
				result <- out
			}()
			<-entered
			switch scenario {
			case "revoke", "revoke and reenable":
				if code := f.call("DELETE", "/api/v1/team/github-app/automation?repository_id=701", owner.auth, nil, nil); code != http.StatusNoContent {
					t.Fatalf("revoke = %d", code)
				}
				if scenario == "revoke and reenable" {
					enableAutomation(t, f, owner)
				}
			case "suspend":
				if code, out := f.deliver("installation", map[string]any{"action": "suspend", "installation": map[string]any{"id": 7, "app_id": githubapptest.AppID}}, ""); code != http.StatusOK {
					t.Fatalf("suspend = %d,%+v", code, out)
				}
			case "unbind":
				if code := f.call("DELETE", "/api/v1/team/github-app/installations/7", owner.auth, nil, nil); code != http.StatusNoContent {
					t.Fatalf("unbind = %d", code)
				}
			}
			unblock()
			if out := <-result; out["status"] != "ignored" {
				t.Fatalf("revoked discovery dispatched = %+v", out)
			}
			if len(f.triggers(owner.team)) != 0 {
				t.Fatal("revoked discovery persisted a trigger")
			}
		})
	}
}

const automationConfig = `pipelines:
  - name: deploy
    entrypoint: Deploy
    on:
      push:
        branches: [main]
      pull_request:
        actions: [synchronize]
        branches: [main]
`

func automationPreview(t *testing.T, f *appFixture, who signedIn) map[string]any {
	t.Helper()
	var out map[string]any
	if code := f.call("GET", "/api/v1/team/github-app/automation?repository=acme/widgets", who.auth, nil, &out); code != http.StatusOK {
		t.Fatalf("preview = %d,%+v", code, out)
	}
	return out
}

func TestGitHubAppAutomationFailsClosedAndRemovalRevokesConsent(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig))
	enableAutomation(t, f, olga)
	f.app.FailContents(1)
	if out := automationPreview(t, f, olga); out["status"] != "unavailable" || out["enabled"] != true {
		t.Fatalf("temporary error=%+v", out)
	}
	f.app.FailContents(1)
	if code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", strings.Repeat("6", 40)), ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("unreadable config dispatch=%d,%+v", code, out)
	}
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte("pipelines: [invalid"))
	if code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", strings.Repeat("7", 40)), ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid config dispatch=%d,%+v", code, out)
	}
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", nil)
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", strings.Repeat("8", 40)), ""); out["status"] != "ignored" {
		t.Fatalf("removed config dispatch=%+v", out)
	}
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig))
	if _, out := f.deliver("push", pushPayload(7, 999, "acme/widgets", strings.Repeat("9", 40)), ""); out["status"] != "ignored" {
		t.Fatalf("unconsented numeric identity dispatch=%+v", out)
	}
	if code, _ := f.deliver("push", pushPayload(7, 701, "acme/plans", strings.Repeat("c", 40)), ""); code < 400 {
		t.Fatalf("mismatched repository identity dispatch=%d", code)
	}
	stale := pushedAt(pushPayload(7, 701, "acme/widgets", strings.Repeat("b", 40)), time.Now().Add(-5*time.Minute))
	if _, out := f.deliver("push", stale, ""); out["status"] != "ignored" {
		t.Fatalf("pre-consent delivery=%+v", out)
	}
	f.app.SetRepos(7, githubapptest.Repo{ID: 702, FullName: "acme/plans"})
	payload := map[string]any{"action": "removed", "installation": map[string]any{"id": 7}, "repositories_removed": []map[string]any{{"id": 701, "full_name": "acme/widgets"}}}
	if code, out := f.deliver("installation_repositories", payload, ""); code != http.StatusOK {
		t.Fatalf("repository removal=%d,%+v", code, out)
	}
	f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: "acme/widgets"}, githubapptest.Repo{ID: 702, FullName: "acme/plans"})
	if out := automationPreview(t, f, olga); out["enabled"] != false {
		t.Fatalf("re-added repo regained consent=%+v", out)
	}
	if len(f.triggers(olga.team)) != 0 {
		t.Fatal("invalid configuration or identity dispatched work")
	}
}

func enableAutomation(t *testing.T, f *appFixture, who signedIn) {
	t.Helper()
	if code := f.call("PUT", "/api/v1/team/github-app/automation", who.auth, map[string]string{"repository": "acme/widgets"}, nil); code != http.StatusOK {
		t.Fatalf("enable = %d", code)
	}
}

func TestGitHubAppAutomationRequiresConsentAndUsesDefaultBranch(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.connect(bob, 502, 8, nil)
	reader := f.user("automation-reader", "automation-reader@example.com")
	f.join(olga, reader, "automation-reader@example.com", "reader")
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig))
	out := automationPreview(t, f, olga)
	if out["enabled"] != false || out["status"] != "ready" || out["source_sha"] != headSHA {
		t.Fatalf("initial discovery=%+v", out)
	}
	if code := f.call("GET", "/api/v1/team/github-app/automation?repository=acme/widgets", reader.auth, nil, nil); code != http.StatusOK {
		t.Fatalf("reader discovery=%d", code)
	}
	if code := f.call("PUT", "/api/v1/team/github-app/automation", reader.auth, map[string]string{"repository": "acme/widgets"}, nil); code != http.StatusForbidden {
		t.Fatalf("reader consent=%d", code)
	}
	if code := f.call("DELETE", "/api/v1/team/github-app/automation?repository_id=701", reader.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("reader revocation=%d", code)
	}
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), ""); out["status"] != "ignored" {
		t.Fatalf("unconsented push=%+v", out)
	}
	if code := f.call("PUT", "/api/v1/team/github-app/automation", bob.auth, map[string]string{"repository": "acme/widgets"}, nil); code != http.StatusNotFound {
		t.Fatalf("other team's enable=%d", code)
	}
	enableAutomation(t, f, olga)
	feature := pushPayload(7, 701, "acme/widgets", strings.Repeat("2", 40))
	feature["ref"] = "refs/heads/feature"
	f.app.SetFile("acme/widgets", strings.Repeat("2", 40), ".sparkwing/sparkwing.yaml", []byte(strings.ReplaceAll(automationConfig, "branches: [main]", "branches: [feature]")))
	if _, out := f.deliver("push", feature, ""); out["status"] != "ignored" {
		t.Fatalf("feature config authorized push=%+v", out)
	}
	main := pushPayload(7, 701, "acme/widgets", strings.Repeat("3", 40))
	if _, out := f.deliver("push", main, ""); out["status"] != "dispatched" {
		t.Fatalf("consented main push=%+v", out)
	}
	if _, out := f.deliver("push", main, ""); out["status"] != "duplicate" {
		t.Fatalf("replayed push=%+v", out)
	}
	if len(f.triggers(olga.team)) != 1 || len(f.triggers(bob.team)) != 0 {
		t.Fatal("dispatch escaped its team or duplicated")
	}
	pr := prPayload(7, 701, "acme/widgets", 701)
	if _, out := f.deliver("pull_request", pr, ""); out["status"] != "ignored" {
		t.Fatalf("excluded PR action=%+v", out)
	}
	pr["action"] = "synchronize"
	if _, out := f.deliver("pull_request", pr, ""); out["status"] != "dispatched" {
		t.Fatalf("included PR action=%+v", out)
	}
	pr["pull_request"].(map[string]any)["base"].(map[string]any)["ref"] = "feature"
	if _, out := f.deliver("pull_request", pr, ""); out["status"] != "ignored" {
		t.Fatalf("excluded PR base=%+v", out)
	}
	if code := f.call("DELETE", "/api/v1/team/github-app/automation?repository_id=701", olga.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke=%d", code)
	}
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", strings.Repeat("4", 40)), ""); out["status"] != "ignored" {
		t.Fatalf("revoked push=%+v", out)
	}
}

func TestGitHubAppAutomationConfigErrorsAndManualOverride(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig))
	enableAutomation(t, f, olga)
	if code := f.subscribe(olga, "acme/widgets", "deploy", map[string]any{"push": false, "pull_request": true}); code != http.StatusOK {
		t.Fatalf("manual subscription=%d", code)
	}
	out := automationPreview(t, f, olga)
	if out["pipelines"].([]any)[0].(map[string]any)["manual_override"] != true {
		t.Fatalf("override discovery=%+v", out)
	}
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", strings.Repeat("5", 40)), ""); out["status"] != "ignored" {
		t.Fatalf("config broadened manual policy=%+v", out)
	}
	pr := prPayload(7, 701, "acme/widgets", 701)
	pr["action"] = "synchronize"
	if _, out := f.deliver("pull_request", pr, ""); out["status"] != "dispatched" {
		t.Fatalf("manual/config duplicate=%+v", out)
	}
	if len(f.triggers(olga.team)) != 1 {
		t.Fatal("same pipeline dispatched twice")
	}
	for _, tc := range []struct{ config, status string }{
		{"pipelines: [invalid", "invalid"},
		{strings.Replace(automationConfig, "branches: [main]", "paths: [src/*]", 1), "unsupported"},
		{strings.Replace(automationConfig, "[synchronize]", "[labeled]", 1), "unsupported"},
		{strings.Replace(automationConfig, "branches: [main]", "branches: ['**']", 1), "unsupported"},
		{strings.Replace(automationConfig, "branches: [main]", "branches: ['!main']", 1), "unsupported"},
		{automationConfig + "---\npipelines: []\n", "invalid"},
	} {
		f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(tc.config))
		out := automationPreview(t, f, olga)
		if out["status"] != tc.status || out["enabled"] != true {
			t.Fatalf("bad discovery=%+v", out)
		}
		if code := f.call("PUT", "/api/v1/team/github-app/automation", olga.auth, map[string]string{"repository": "acme/widgets"}, nil); code != http.StatusUnprocessableEntity {
			t.Fatalf("bad config enable=%d", code)
		}
		pr := prPayload(7, 701, "acme/widgets", 701)
		pr["action"] = "synchronize"
		pr["number"] = 30 + len(f.triggers(olga.team))
		if _, out := f.deliver("pull_request", pr, ""); out["status"] != "dispatched" {
			t.Fatalf("bad config suppressed manual row=%+v", out)
		}
	}
	f.app.SetCommit("acme/widgets", "main", strings.Repeat("a", 40))
	if out := automationPreview(t, f, olga); out["status"] != "missing" || out["enabled"] != true {
		t.Fatalf("removed config=%+v", out)
	}
	for _, query := range []string{"?repository=bad", "?repository=", "?oops=widgets"} {
		if code := f.call("GET", "/api/v1/team/github-app/automation"+query, olga.auth, nil, nil); code != http.StatusBadRequest {
			t.Fatalf("malformed %s=%d", query, code)
		}
	}
}
