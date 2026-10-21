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
	const execution = "0123456789abcdef0123456789abcdef01234567"
	const pinned = "abcdef0123456789abcdef0123456789abcdef01"
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		pin   string
		trust runspec.TrustClass
	}{
		{name: "execution commit", trust: runspec.TrustSchedule},
		{name: "branch pin", pin: "origin/code", trust: runspec.TrustAsserted},
		{name: "commit pin", pin: pinned, trust: runspec.TrustSchedule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups := map[string]int{}
			cfg := githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/acme/widgets/installation":
					body = `{"id":7,"app_id":1}`
				case "POST /app/installations/7/access_tokens":
					body = fmt.Sprintf(`{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
				case "GET /installation/repositories":
					body = `{"total_count":1,"repositories":[{"id":701,"full_name":"acme/widgets","owner":{"id":42,"login":"acme","type":"Organization"}}]}`
				case "GET /repositories/701":
					body = `{"id":701,"default_branch":"main"}`
				case "GET /repos/acme/widgets/commits/main":
					lookups["main"]++
					body = `{"sha":"` + execution + `"}`
				case "GET /repos/acme/widgets/commits/code":
					lookups["code"]++
					body = `{"sha":"` + pinned + `"}`
				case "GET /repos/acme/widgets/contents/.sparkwing/sparkwing.yaml":
					if got := r.URL.Query().Get("ref"); got != execution {
						t.Fatalf("configuration read at %q, want fire-time execution commit", got)
					}
					body = "pipelines:\n  - name: nightly\n    entrypoint: Nightly\n"
					if tc.pin != "" {
						body += "    source: " + tc.pin + "\n"
					}
				default:
					t.Fatalf("unexpected GitHub call: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}}
			e, tenant := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithGitHubApp(cfg) })
			engineSQL(t, e.Store, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',42,'acme','Organization',1,1)`)
			engineSQL(t, e.Store, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a','gh:701',701,'acme/widgets',7,'owner',1)`)
			row, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{ID: "nightly", RepoPath: "https://github.com/acme/widgets.git", Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC", Where: store.CronWhereController, GitBranch: "main", GitHubInstallationID: 7, GitHubRepositoryID: 701}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `UPDATE cron_schedules SET github_installation_id=7,github_repository_id=701`)
			row, err = tenant.GetCronSchedule(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			admitted, err := tenant.AdmitScheduledRun(t.Context(), row, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			request := admitted.Request.Request
			wantSource := execution
			if tc.pin != "" {
				wantSource = pinned
			}
			if request.Git.SHA != execution || request.Source.Ref != wantSource || admitted.Request.Provenance.Trust() != tc.trust || lookups["main"] != 1 {
				t.Fatalf("fire execution=%s source=%s trust=%s lookups=%v, want execution=%s source=%s trust=%s", request.Git.SHA, request.Source.Ref, admitted.Request.Provenance.Trust(), lookups, execution, wantSource, tc.trust)
			}
			engineSQL(t, e.Store, `DELETE FROM github_app_automation`)
			if _, err := tenant.AdmitScheduledRun(t.Context(), row, time.Now().Add(time.Minute)); !errors.Is(err, store.ErrGitHubAppAutomationRevoked) {
				t.Fatalf("fire after revoked consent = %v", err)
			}
		})
	}
}
