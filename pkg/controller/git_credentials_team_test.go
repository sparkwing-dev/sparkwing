package controller_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func (f *credFixture) cloudRun(owner signedIn, runID, repoURL string) string {
	f.t.Helper()
	auth, prefix := f.nodeClaim(owner, runID, repoURL, 0)
	if err := seedLegacyMeteredCredential(f.store, prefix); err != nil {
		f.t.Fatal(err)
	}
	return auth
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
	covered, prefix := f.nodeClaim(f.olga, "run-widgets", "https://github.com/acme/widgets.git", 701)
	if err := seedLegacyMeteredCredential(f.store, prefix); err != nil {
		t.Fatal(err)
	}
	got, code := f.sourceCredential(covered, "run-widgets")
	if code != http.StatusOK || got.Kind != "github_app" || got.Secret != "" || !f.app.TokenCovers(got.Token, "acme/widgets") {
		t.Fatalf("covered repository = %d %+v; want the App token", code, got)
	}
	if rels := f.releases(f.olga); len(rels) != 1 || rels[0].RunID != "run-widgets" {
		t.Fatalf("audit = %+v; want the App token's row alone", rels)
	}
	uncovered := f.cloudRun(f.olga, "run-else", "https://github.com/someone/else.git")
	got, code = f.sourceCredential(uncovered, "run-else")
	if code != http.StatusOK || got.Kind != "https" || got.Secret != "github_pat_x" {
		t.Fatalf("uncovered repository = %d %+v; want the stored credential", code, got)
	}
}

func seedLegacyMeteredCredential(st *store.Store, prefix string) error {
	_, err := st.DB().ExecContext(context.Background(), "UPDATE tokens SET metered=1 WHERE prefix=?", prefix)
	return err
}
