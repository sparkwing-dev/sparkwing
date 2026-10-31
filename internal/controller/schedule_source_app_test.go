package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestAppScheduleFireResolvesBothRevisionsThroughGitHub(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 3.1s measured in the short class; the full suite runs it")
	}
	const execution = "0123456789abcdef0123456789abcdef01234567"
	const pinned = "abcdef0123456789abcdef0123456789abcdef01"
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		pin           string
		trust         runspec.TrustClass
		refusal       bool
		missingBranch bool
		matchingTag   bool
		locked        bool
		unreachable   bool
	}{
		{name: "execution commit", trust: runspec.TrustSchedule},
		{name: "branch pin", pin: "origin/code", trust: runspec.TrustAsserted},
		{name: "commit pin", pin: pinned, trust: runspec.TrustSchedule},
		{name: "ambiguous branch and tag pin", pin: "main", refusal: true},
		{name: "short commit prefix pin", pin: pinned[:12], refusal: true},
		{name: "qualified revision expression pin", pin: "refs/heads/main~1", refusal: true},
		{name: "explicit branch pin", pin: "refs/heads/main", trust: runspec.TrustSchedule},
		{name: "explicit tag pin", pin: "refs/tags/main", trust: runspec.TrustAsserted},
		{name: "matching branch and tag pin", pin: "main", matchingTag: true, trust: runspec.TrustSchedule},
		{name: "tag heads/main cannot replace missing execution branch", missingBranch: true, refusal: true},
		{name: "reachable locked commit", locked: true, trust: runspec.TrustAsserted},
		{name: "fork-only locked commit", locked: true, unreachable: true, trust: runspec.TrustAsserted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups := map[string]int{}
			selectedExecution := execution
			if tc.locked {
				selectedExecution = pinned
			}
			cfg := githubapp.Config{AppID: 1, PrivateKey: key, WebhookSecret: "hook", HTTP: &http.Client{Transport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				status := http.StatusOK
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/acme/widgets/installation":
					body = `{"id":7,"app_id":1}`
				case "POST /app/installations/7/access_tokens":
					body = fmt.Sprintf(`{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
				case "GET /installation/repositories":
					body = `{"total_count":1,"repositories":[{"id":701,"full_name":"acme/widgets","owner":{"id":42,"login":"acme","type":"Organization"}}]}`
				case "GET /repositories/701":
					body = `{"id":701,"full_name":"acme/widgets","default_branch":"main"}`
				case "GET /repos/acme/widgets/git/ref/heads/main":
					lookups["main"]++
					body = `{"ref":"refs/heads/main","object":{"type":"commit","sha":"` + execution + `"}}`
					if tc.missingBranch {
						status = http.StatusNotFound
					}
				case "GET /repos/acme/widgets/git/ref/tags/main":
					sha := pinned
					if tc.matchingTag {
						sha = execution
					}
					body = `{"ref":"refs/tags/main","object":{"type":"commit","sha":"` + sha + `"}}`
				case "GET /repos/acme/widgets/commits/heads/main", "GET /repos/acme/widgets/commits/heads/main~1":
					// The commits endpoint accepts the attacker's tag literally named heads/main.
					body = `{"sha":"` + pinned + `"}`
				case "GET /repos/acme/widgets/commits/" + pinned:
					body = `{"sha":"` + pinned + `"}`
				case "GET /repos/acme/widgets/git/ref/heads/code":
					lookups["code"]++
					body = `{"ref":"refs/heads/code","object":{"type":"commit","sha":"` + pinned + `"}}`
				case "GET /repos/acme/widgets/git/ref/tags/code":
					status = http.StatusNotFound
				case "GET /repos/acme/widgets/contents/.sparkwing/sparkwing.yaml":
					if got := r.URL.Query().Get("ref"); got != selectedExecution {
						t.Fatalf("configuration read at %q, want fire-time execution commit", got)
					}
					body = "pipelines:\n  - name: nightly\n    entrypoint: Nightly\n"
					if tc.pin != "" {
						body += "    source: " + tc.pin + "\n"
					}
				case "GET /repos/acme/widgets/compare/" + execution + "...refs/heads/main", "GET /repos/acme/widgets/compare/" + pinned + "...refs/heads/main", "GET /repos/acme/widgets/compare/" + pinned + "...refs/heads/code", "GET /repos/acme/widgets/compare/" + pinned + "...refs/tags/main":
					body = `{"status":"ahead"}`
					if tc.unreachable {
						body = `{"status":"diverged"}`
					}
				case "GET /repos/acme/widgets/git/ref/heads/" + pinned[:12], "GET /repos/acme/widgets/git/ref/tags/" + pinned[:12], "GET /repos/acme/widgets/git/ref/heads/main~1":
					status = http.StatusNotFound
				default:
					t.Fatalf("unexpected GitHub call: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}}
			e, tenant := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithGitHubApp(cfg) })
			engineSQL(t, e.Store, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',42,'acme','Organization',1,1)`)
			engineSQL(t, e.Store, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a','gh:701',701,'acme/widgets',7,'owner',1)`)
			locked := ""
			if tc.locked {
				locked = pinned
			}
			row, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{ID: "nightly", RepoPath: "https://github.com/acme/widgets.git", Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC", Where: store.CronWhereController, GitBranch: "main", GitHubInstallationID: 7, GitHubRepositoryID: 701, LockedRef: locked})
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `UPDATE cron_schedules SET github_installation_id=7,github_repository_id=701`)
			row, err = tenant.GetCronSchedule(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			admitted, err := tenant.AdmitScheduledRun(t.Context(), row, time.Now())
			if tc.refusal {
				if err == nil {
					t.Fatal("ambiguous or abbreviated source pin admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			request := admitted.Request.Request
			wantSource := selectedExecution
			if tc.pin != "" && tc.pin != "refs/heads/main" && !tc.matchingTag {
				wantSource = pinned
			}
			if request.Git.SHA != selectedExecution || request.Source.Ref != wantSource || admitted.Request.Provenance.Trust() != tc.trust || !tc.locked && lookups["main"] == 0 {
				t.Fatalf("fire execution=%s source=%s trust=%s lookups=%v, want execution=%s source=%s trust=%s", request.Git.SHA, request.Source.Ref, admitted.Request.Provenance.Trust(), lookups, execution, wantSource, tc.trust)
			}
			if admitted.Request.Provenance.SourceCredentialVerified() == tc.unreachable {
				t.Fatalf("source credential compare=%v, reachable=%v", admitted.Request.Provenance.SourceCredentialVerified(), !tc.unreachable)
			}
			if tc.locked {
				proof := admitted.Request.Provenance
				if (proof.ProvenRepository() == "gh:701") == tc.unreachable || proof.SourceCredentialVerified() == tc.unreachable {
					t.Fatalf("locked commit: proven=%q credential checked=%v reachable=%v", proof.ProvenRepository(), proof.SourceCredentialVerified(), !tc.unreachable)
				}
				if _, _, ref, sha := proof.OIDC(); ref != "" || sha != "" {
					t.Fatalf("locked commit acquired a branch-head claim: %q %q", ref, sha)
				}
			}
			engineSQL(t, e.Store, `DELETE FROM github_app_automation`)
			if _, err := tenant.AdmitScheduledRun(t.Context(), row, time.Now().Add(time.Minute)); !errors.Is(err, store.ErrGitHubAppAutomationRevoked) {
				t.Fatalf("fire after revoked consent = %v", err)
			}
		})
	}
}
