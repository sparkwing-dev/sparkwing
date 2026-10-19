package controller_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type credFixture struct {
	*appFixture
	olga signedIn
}

func newCredFixture(t *testing.T) *credFixture {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithSecretsCipher(cipher)
	})
	return &credFixture{appFixture: f, olga: f.ghUser(501, "olga-gh")}
}

func deployKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func (f *credFixture) put(who signedIn, body map[string]any) (controller.GitCredentialJSON, int) {
	f.t.Helper()
	var out controller.GitCredentialJSON
	code := f.call("POST", "/api/v1/team/git-credentials", who.auth, body, &out)
	return out, code
}

type releaseRow struct {
	Host        string `json:"host"`
	RunID       string `json:"run_id"`
	Runner      string `json:"runner"`
	TokenPrefix string `json:"token_prefix"`
}

func (f *credFixture) releases(who signedIn) []releaseRow {
	f.t.Helper()
	var out struct {
		Releases []releaseRow `json:"releases"`
	}
	if code := f.call("GET", "/api/v1/team/git-credentials/releases", who.auth, nil, &out); code != http.StatusOK {
		f.t.Fatalf("releases = %d", code)
	}
	return out.Releases
}

func TestTeamGitCredentialLosesToTheAppWhenTheAppCovers(t *testing.T) {
	f := newCredFixture(t)
	f.connect(f.olga, 501, 7, acmeAdmin)
	if _, code := f.put(f.olga, map[string]any{"host": "github.com", "kind": "https", "token": "github_pat_x"}); code != http.StatusOK {
		t.Fatalf("put = %d", code)
	}
	covered, _ := f.nodeClaim(f.olga, "run-widgets", "https://github.com/acme/widgets.git", 701)
	got, code := f.sourceCredential(covered, "run-widgets")
	if code != http.StatusOK || got.Kind != "github_app" || got.Secret == "" || got.Secret == "github_pat_x" || !f.app.TokenCovers(got.Secret, "acme/widgets") {
		t.Fatalf("covered repository = %d %+v; want the App token", code, got)
	}
	if rels := f.releases(f.olga); len(rels) != 1 || rels[0].RunID != "run-widgets" {
		t.Fatalf("audit = %+v; want the App token's row alone", rels)
	}
	unsubscribed, _ := f.nodeClaim(f.olga, "run-else", "https://github.com/acme/plans.git", 0)
	got, code = f.sourceCredential(unsubscribed, "run-else")
	if code != http.StatusOK || got.Kind != "https" || got.Secret != "github_pat_x" {
		t.Fatalf("unsubscribed repository = %d %+v; want the stored credential", code, got)
	}
}

func TestTeamGitCredentialsNeverReachAPersonalMachine(t *testing.T) {
	f := newCredFixture(t)
	f.connect(f.olga, 501, 7, acmeAdmin)
	if _, code := f.put(f.olga, map[string]any{"host": "github.com", "kind": "https", "token": "github_pat_x"}); code != http.StatusOK {
		t.Fatalf("put = %d", code)
	}
	for _, tc := range []struct {
		run, repo string
		id        int64
	}{
		{"run-app", "https://github.com/acme/widgets.git", 701},
		{"run-stored", "https://github.com/acme/plans.git", 0},
	} {
		_, laptop, _ := f.claimRunNodeOn(f.olga, tc.run+"-laptop", tc.repo, tc.id, true)
		var refused struct {
			Error struct {
				Details struct {
					Reason string `json:"reason"`
				} `json:"details"`
			} `json:"error"`
		}
		code := f.call("POST", "/api/v1/runs/"+tc.run+"-laptop/source-credential", laptop, nil, &refused)
		if code != http.StatusForbidden || refused.Error.Details.Reason != "personal_machine" {
			t.Fatalf("%s on a personal machine = %d %v", tc.run, code, refused)
		}
		_, shared, _ := f.claimRunNodeOn(f.olga, tc.run+"-shared", tc.repo, tc.id, false)
		if got, code := f.sourceCredential(shared, tc.run+"-shared"); code != http.StatusOK || got.Secret == "" {
			t.Fatalf("%s on a shared runner = %d %+v", tc.run, code, got)
		}
	}
	for _, row := range f.releases(f.olga) {
		if strings.HasSuffix(row.RunID, "-laptop") {
			t.Fatalf("personal machine release audited: %+v", row)
		}
	}
}
