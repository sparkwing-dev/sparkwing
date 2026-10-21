package githubapp_test

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
)

type refTransport func(*http.Request) (*http.Response, error)

func (f refTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveCommitUsesExactReferenceAndPeelsTags(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const other = "abcdef0123456789abcdef0123456789abcdef01"
	const tag = "1111111111111111111111111111111111111111"
	const nested = "2222222222222222222222222222222222222222"
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ref, reference, tagBody, nestedBody, want string
	}{
		{name: "HEAD follows exact default branch", ref: "HEAD", reference: `{"ref":"refs/heads/main","object":{"type":"commit","sha":"` + commit + `"}}`, want: commit},
		{name: "full commit identity", ref: commit, reference: `{"sha":"` + commit + `"}`, want: commit},
		{name: "full commit identity changed", ref: commit, reference: `{"sha":"` + other + `"}`},
		{name: "branch wins over tag named heads/main", ref: "refs/heads/main", reference: `{"ref":"refs/heads/main","object":{"type":"commit","sha":"` + commit + `"}}`, want: commit},
		{name: "branch and tag names differ", ref: "refs/heads/main", reference: `{"ref":"refs/tags/main","object":{"type":"commit","sha":"` + other + `"}}`},
		{name: "lightweight tag", ref: "refs/tags/release", reference: `{"ref":"refs/tags/release","object":{"type":"commit","sha":"` + other + `"}}`, want: other},
		{name: "nested annotated tag", ref: "refs/tags/release", reference: `{"ref":"refs/tags/release","object":{"type":"tag","sha":"` + tag + `"}}`, tagBody: `{"sha":"` + tag + `","object":{"type":"tag","sha":"` + nested + `"}}`, nestedBody: `{"sha":"` + nested + `","object":{"type":"commit","sha":"` + other + `"}}`, want: other},
		{name: "tag identity changed", ref: "refs/tags/release", reference: `{"ref":"refs/tags/release","object":{"type":"tag","sha":"` + tag + `"}}`, tagBody: `{"sha":"` + other + `","object":{"type":"commit","sha":"` + other + `"}}`},
		{name: "tag points to blob", ref: "refs/tags/release", reference: `{"ref":"refs/tags/release","object":{"type":"blob","sha":"` + other + `"}}`},
		{name: "tag cycle", ref: "refs/tags/release", reference: `{"ref":"refs/tags/release","object":{"type":"tag","sha":"` + tag + `"}}`, tagBody: `{"sha":"` + tag + `","object":{"type":"tag","sha":"` + tag + `"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exactRef := tc.ref
			if exactRef == "HEAD" {
				exactRef = "refs/heads/main"
			}
			client := githubapp.New(githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: refTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/7/access_tokens":
					body = fmt.Sprintf(`{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
				case "GET /repos/acme/widgets":
					body = `{"default_branch":"main"}`
				case "GET /repos/acme/widgets/commits/" + commit:
					body = tc.reference
				case "GET /repos/acme/widgets/git/ref/" + strings.TrimPrefix(exactRef, "refs/"):
					body = tc.reference
				case "GET /repos/acme/widgets/git/tags/" + tag:
					body = tc.tagBody
				case "GET /repos/acme/widgets/git/tags/" + nested:
					body = tc.nestedBody
				case "GET /repos/acme/widgets/commits/heads/main":
					body = `{"sha":"` + other + `"}`
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}})
			got, err := client.ResolveCommit(t.Context(), 7, "acme", "widgets", tc.ref)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("commit=%s err=%v, want %s", got, err, tc.want)
			}
		})
	}
}
