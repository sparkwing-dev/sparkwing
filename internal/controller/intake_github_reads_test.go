package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// Each GitHub request is a serial round trip on the trigger's critical path, so
// one intake must not repeat an identical read or token mint.
func TestIntakeReadsEachGitHubFactOnce(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	const sha = "0123456789abcdef0123456789abcdef01234567"
	s, session := credentialSessionServer(t)
	teststore.SeedGitHubInstallation(t, s.store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", ConnectedBy: "member", GitHubUserID: 71}, time.Now())
	var mu sync.Mutex
	requests := map[string]int{}
	s.WithGitHubApp(githubapp.Config{AppID: 1, Slug: "test", ClientID: "id", ClientSecret: "secret", WebhookSecret: "webhook", PrivateKey: key, HTTP: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		request := r.Method + " " + r.URL.RequestURI()
		if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			request += " " + string(body)
		}
		mu.Lock()
		requests[request]++
		mu.Unlock()
		code, payload := http.StatusOK, ""
		switch r.URL.Path {
		case "/repos/acme/app/installation":
			payload = `{"id":7,"app_id":1,"account":{"id":70,"login":"acme","type":"Organization"}}`
		case "/app/installations/7/access_tokens":
			payload = `{"token":"private","expires_at":"2100-01-01T00:00:00Z"}`
		case "/installation/repositories":
			payload = `{"total_count":1,"repositories":[{"id":42,"full_name":"acme/app","default_branch":"main","owner":{"id":70,"login":"acme"}}]}`
		case "/repos/acme/app", "/repositories/42":
			payload = `{"id":42,"full_name":"acme/app","default_branch":"main","owner":{"id":70,"login":"acme"}}`
		case "/repos/acme/app/git/ref/heads/main":
			payload = `{"ref":"refs/heads/main","object":{"type":"commit","sha":"` + sha + `"}}`
		case "/repos/acme/app/commits/" + sha:
			payload = `{"sha":"` + sha + `"}`
		case "/repos/acme/app/compare/" + sha + "...refs/heads/main":
			payload = `{"status":"ahead"}`
		case "/repos/acme/app/contents/.sparkwing/sparkwing.yaml":
			code, payload = http.StatusNotFound, `{}`
		default:
			code, payload = http.StatusNotFound, `{}`
		}
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
	})}})
	body, err := json.Marshal(controllerwire.CreateRunRequest{IdempotencyKey: "reads", Request: runspec.Request{
		Version: 1, Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
		Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:42"}, Ref: sha, SHA: sha},
		Source: runspec.Source{Ref: sha, Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app", PipelineDir: ".sparkwing"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	w := resumeHTTP(s, session, "/api/v1/runs", string(body))
	if w.Code != http.StatusCreated {
		t.Fatalf("intake: HTTP %d %s", w.Code, w.Body)
	}
	total, repeated := 0, []string{}
	for request, n := range requests {
		total += n
		if n > 1 {
			repeated = append(repeated, fmt.Sprintf("%dx %s", n, request))
		}
	}
	sort.Strings(repeated)
	if len(repeated) > 0 {
		t.Fatalf("one intake made %d GitHub requests for %d distinct reads; repeated:\n%s", total, len(requests), strings.Join(repeated, "\n"))
	}
}
