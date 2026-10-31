package githubapp_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
)

func TestCommitReachableRequiresRepositoryRefContainingCommit(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"ahead", "identical", "behind", "diverged", "", "unknown", "missing"} {
		t.Run(status, func(t *testing.T) {
			compares := 0
			client := githubapp.New(githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: refTransport(func(r *http.Request) (*http.Response, error) {
				code, body := http.StatusOK, ""
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/7/access_tokens":
					var input struct {
						Repositories []int64           `json:"repository_ids"`
						Permissions  map[string]string `json:"permissions"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if len(input.Repositories) != 1 || input.Repositories[0] != 42 || len(input.Permissions) != 1 || input.Permissions["contents"] != "read" {
						t.Fatalf("compare token exceeded repository read: %+v", input)
					}
					body = `{"token":"private","expires_at":"2100-01-01T00:00:00Z"}`
				case "GET /repos/acme/widgets/compare/" + sha + "...refs/heads/main":
					compares++
					if r.Header.Get("Authorization") != "Bearer private" || r.URL.Query().Get("per_page") != "1" {
						t.Fatalf("compare request: %s %+v", r.URL, r.Header)
					}
					body = fmt.Sprintf(`{"status":%q}`, status)
					if status == "missing" {
						code, body = http.StatusNotFound, `{}`
					}
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}})
			got, err := client.CommitReachable(t.Context(), 7, 42, "acme", "widgets", sha, "refs/heads/main")
			want := status == "ahead" || status == "identical"
			unavailable := status == "" || status == "unknown"
			if (err != nil) != unavailable || unavailable && !errors.Is(err, githubapp.ErrUnavailable) || got != want || compares != 1 {
				t.Fatalf("reachable=%v err=%v compares=%d, want %v", got, err, compares, want)
			}
		})
	}
	client := githubapp.New(githubapp.Config{})
	for _, ref := range []string{sha, "main", "refs/pull/1/head", "refs/heads/fork:main", ""} {
		if proven, err := client.CommitReachable(t.Context(), 7, 42, "acme", "widgets", sha, ref); proven || err == nil {
			t.Fatalf("caller-selected ref %q was accepted: %v %v", ref, proven, err)
		}
	}
}

func TestCommitReachableParsesLargeFirstPageFiles(t *testing.T) {
	const base = "0123456789abcdef0123456789abcdef01234567"
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	app := githubapptest.New(t)
	app.AddInstallation(githubapptest.Installation{ID: 7, Account: githubapp.Account{ID: 70, Login: "acme", Type: "Organization"}, Repos: []githubapptest.Repo{{ID: 42, FullName: "acme/widgets"}}})
	app.SetCommit("acme/widgets", "main", head)
	app.SetComparison("acme/widgets", base, "refs/heads/main", "ahead")
	files := make([]map[string]any, 300)
	for i := range files {
		files[i] = map[string]any{"filename": fmt.Sprintf("file-%d.txt", i), "status": "modified", "patch": strings.Repeat("+line\n", 700)}
	}
	app.SetComparisonFiles("acme/widgets", base, "refs/heads/main", files)
	client := githubapp.New(app.Config())
	if reachable, err := client.CommitReachable(t.Context(), 7, 42, "acme", "widgets", base, "refs/heads/main"); err != nil || !reachable {
		t.Fatalf("large comparison: reachable=%v err=%v", reachable, err)
	}
}
