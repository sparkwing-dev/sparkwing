package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestRawCommitIntakeSealsOnlyRepositoryReachableCode(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, status := range []string{"ahead", "identical", "diverged", "behind"} {
		t.Run(status, func(t *testing.T) {
			s, session := credentialSessionServer(t)
			teststore.SeedGitHubInstallation(t, s.store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", ConnectedBy: "member", GitHubUserID: 71}, time.Now())
			compares := 0
			s.WithGitHubApp(githubapp.Config{AppID: 1, Slug: "test", ClientID: "id", ClientSecret: "secret", WebhookSecret: "webhook", PrivateKey: key, HTTP: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
				code, payload := http.StatusOK, ""
				switch r.URL.Path {
				case "/repos/acme/app/installation":
					payload = `{"id":7,"app_id":1,"account":{"id":70,"login":"acme","type":"Organization"}}`
				case "/app/installations/7/access_tokens":
					payload = `{"token":"private","expires_at":"2100-01-01T00:00:00Z"}`
				case "/installation/repositories":
					payload = `{"total_count":1,"repositories":[{"id":42,"full_name":"acme/app","default_branch":"main","owner":{"id":70,"login":"acme"}}]}`
				case "/repos/acme/app/compare/" + sha + "...refs/heads/main":
					compares++
					payload = fmt.Sprintf(`{"status":%q}`, status)
				case "/repos/acme/app/contents/.sparkwing/sparkwing.yaml":
					if r.URL.Query().Get("ref") != sha {
						t.Fatalf("configuration read another commit: %s", r.URL)
					}
					code, payload = http.StatusNotFound, `{}`
				default:
					t.Fatalf("unexpected GitHub request %s", r.URL)
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
			})}})
			body, err := json.Marshal(controllerwire.CreateRunRequest{IdempotencyKey: "raw", Request: runspec.Request{
				Version: 1, Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
				Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:42"}, Ref: sha, SHA: sha},
				Source: runspec.Source{Ref: sha, Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app", PipelineDir: ".sparkwing"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			w := resumeHTTP(s, session, "/api/v1/runs", string(body))
			if w.Code != http.StatusCreated {
				t.Fatalf("raw commit intake: HTTP %d %s", w.Code, w.Body)
			}
			var created controllerwire.CreateRunResponse
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			tenant, err := s.store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), created.RunID)
			if err != nil {
				t.Fatal(err)
			}
			wantProof := status == "ahead" || status == "identical"
			proof := run.Request.Provenance
			if compares != 1 || (proof.ProvenRepository() == "gh:42") != wantProof || proof.SourceCredentialVerified() != wantProof {
				t.Fatalf("compares=%d proven=%q credential checked=%v, want proof=%v", compares, proof.ProvenRepository(), proof.SourceCredentialVerified(), wantProof)
			}
			if _, _, ref, commit := proof.OIDC(); ref != "" || commit != "" {
				t.Fatalf("historical commit acquired a head claim: %q %q", ref, commit)
			}
		})
	}
}
