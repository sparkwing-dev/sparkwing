package controller_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	hostKey ssh.PublicKey
	olga    signedIn
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
	hostPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewPublicKey(hostPub)
	if err != nil {
		t.Fatal(err)
	}
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		controller.SetHostKeyScan(s, func(_ context.Context, host string, port int) (ssh.PublicKey, error) {
			if host == "" || port != 22 {
				t.Errorf("host key scan of %s:%d, want port 22", host, port)
			}
			return hostKey, nil
		})
		return s.WithSecretsCipher(cipher)
	})
	return &credFixture{appFixture: f, hostKey: hostKey, olga: f.ghUser(501, "olga")}
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
	auth, prefix := f.runWork(owner, runID, repoURL)
	if err := f.store.SetTokenMetered(context.Background(), prefix, true); err != nil {
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

// An ssh deploy key is stored with the host key the controller read, shown
// by fingerprint, and released only after the owner confirms that
// fingerprint. The release carries the key and the pinned host key, and
// leaves an audit row.
func TestTeamGitCredentialSSHIsUnusableUntilConfirmed(t *testing.T) {
	f := newCredFixture(t)
	key := deployKey(t)
	stored, code := f.put(f.olga, map[string]any{"host": "GitLab.Example.com", "kind": "ssh", "private_key": key})
	want := ssh.FingerprintSHA256(f.hostKey)
	if code != http.StatusOK || stored.Host != "gitlab.example.com" || stored.Fingerprint != want || stored.Confirmed {
		t.Fatalf("put = %d %+v; want an unconfirmed credential pinning %s", code, stored, want)
	}
	if !strings.HasPrefix(stored.PublicKey, "ssh-ed25519 ") || !strings.Contains(stored.HostKey, "gitlab.example.com ssh-ed25519 ") {
		t.Fatalf("put = %+v; want the deploy key's public half and the pinned host key", stored)
	}

	runner := f.cloudRun(f.olga, "run-gl", "git@gitlab.example.com:acme/tools.git")
	got, code := f.gitCredential(runner, "run-gl")
	if code != http.StatusConflict || got.Error != "git_credential_unconfirmed" || got.Secret != "" {
		t.Fatalf("release before confirmation = %d %+v; want 409 git_credential_unconfirmed", code, got)
	}
	if code := f.call("POST", "/api/v1/team/git-credentials/gitlab.example.com/confirm", f.olga.auth,
		map[string]string{"fingerprint": "SHA256:not-the-key"}, nil); code != http.StatusConflict {
		t.Fatalf("confirming another fingerprint = %d, want 409", code)
	}
	if code := f.call("POST", "/api/v1/team/git-credentials/gitlab.example.com/confirm", f.olga.auth,
		map[string]string{"fingerprint": want}, nil); code != http.StatusOK {
		t.Fatalf("confirm = %d", code)
	}
	got, code = f.gitCredential(runner, "run-gl")
	if code != http.StatusOK || got.Kind != "ssh" || got.Host != "gitlab.example.com" || got.Secret != key ||
		!strings.Contains(got.KnownHosts, "gitlab.example.com ssh-ed25519 ") {
		t.Fatalf("release = %d %+v; want the key and its pinned host key", code, got)
	}
	rels := f.releases(f.olga)
	if len(rels) != 1 || rels[0].RunID != "run-gl" || rels[0].Host != "gitlab.example.com" ||
		!strings.Contains(rels[0].Runner, "r-run-gl") || rels[0].TokenPrefix == "" {
		t.Fatalf("audit = %+v; want one row naming the run and the runner", rels)
	}
}

// The credential reaches only a runner holding a live claim on a run of the
// credential's own team whose source is on the credential's host.
func TestTeamGitCredentialStaysWithTheTeamClaimAndHost(t *testing.T) {
	f := newCredFixture(t)
	bob := f.ghUser(502, "bob")
	if _, code := f.put(f.olga, map[string]any{"host": "gitlab.example.com", "kind": "https", "token": "glpat-olga"}); code != http.StatusOK {
		t.Fatalf("put = %d", code)
	}
	olgas := f.cloudRun(f.olga, "run-olga", "https://gitlab.example.com/acme/tools.git")
	if got, code := f.gitCredential(olgas, "run-olga"); code != http.StatusOK || got.Secret != "glpat-olga" ||
		got.Username != "x-access-token" || got.Kind != "https" {
		t.Fatalf("control: the team's cloud runner = %d %+v", code, got)
	}

	bobs := f.cloudRun(bob, "run-bob", "https://gitlab.example.com/bob/tools.git")
	if got, code := f.gitCredential(bobs, "run-bob"); code != http.StatusNotFound || got.Secret != "" || got.Error != "no_source_credential" {
		t.Fatalf("another team's runner on its own run of the host = %d %+v; want no credential", code, got)
	}
	if got, code := f.gitCredential(bobs, "run-olga"); code == http.StatusOK || got.Secret != "" {
		t.Fatalf("another team's runner on the team's run = %d %+v; want a refusal", code, got)
	}

	idle, prefix := f.teamRunner(f.olga, "idle")
	if err := f.store.SetTokenMetered(context.Background(), prefix, true); err != nil {
		t.Fatal(err)
	}
	if got, code := f.gitCredential(idle, "run-olga"); code != http.StatusForbidden || got.Secret != "" {
		t.Fatalf("a cloud runner of the team without a claim = %d %+v; want 403", code, got)
	}

	elsewhere := f.cloudRun(f.olga, "run-elsewhere", "https://gitlab.other.com/acme/tools.git")
	if got, code := f.gitCredential(elsewhere, "run-elsewhere"); code != http.StatusNotFound || got.Secret != "" ||
		!strings.Contains(got.Message, "gitlab.other.com") {
		t.Fatalf("a run on another host = %d %+v; want no credential and a remedy naming its host", code, got)
	}
	if rels := f.releases(f.olga); len(rels) != 1 {
		t.Fatalf("audit = %+v; want only the control's release", rels)
	}
}

// A local machine of the team receives the credential only once a team owner
// opts it in; a cloud runner needs no opt-in.
func TestTeamGitCredentialGoesToLocalMachinesOnlyWhenOptedIn(t *testing.T) {
	f := newCredFixture(t)
	if _, code := f.put(f.olga, map[string]any{"host": "gitlab.example.com", "kind": "https", "token": "glpat-olga"}); code != http.StatusOK {
		t.Fatalf("put = %d", code)
	}
	laptop, prefix := f.runWork(f.olga, "run-laptop", "https://gitlab.example.com/acme/tools.git")
	got, code := f.gitCredential(laptop, "run-laptop")
	if code != http.StatusForbidden || got.Error != "git_credential_not_released" || got.Secret != "" {
		t.Fatalf("a machine not opted in = %d %+v; want 403 git_credential_not_released", code, got)
	}
	path := "/api/v1/team/runner-tokens/" + prefix + "/git-credentials"
	if code := f.call("PUT", path, f.olga.auth, map[string]bool{"enabled": true}, nil); code != http.StatusOK {
		t.Fatalf("opt in = %d", code)
	}
	var tokens []struct {
		Prefix         string `json:"prefix"`
		GitCredentials bool   `json:"git_credentials"`
	}
	f.call("GET", "/api/v1/team/runner-tokens", f.olga.auth, nil, &tokens)
	listed := false
	for _, tok := range tokens {
		listed = listed || (tok.Prefix == prefix && tok.GitCredentials)
	}
	if !listed {
		t.Fatalf("runner tokens = %+v; want %s shown as opted in", tokens, prefix)
	}
	if got, code := f.gitCredential(laptop, "run-laptop"); code != http.StatusOK || got.Secret != "glpat-olga" {
		t.Fatalf("an opted-in machine = %d %+v", code, got)
	}
	if code := f.call("PUT", path, f.olga.auth, map[string]bool{"enabled": false}, nil); code != http.StatusOK {
		t.Fatalf("opt out = %d", code)
	}
	if _, code := f.gitCredential(laptop, "run-laptop"); code != http.StatusForbidden {
		t.Fatalf("after opting out = %d, want 403", code)
	}
}

// Replacing the credential changes what the next release gets, and deleting
// it stops every release after.
func TestTeamGitCredentialRotationAndRevocation(t *testing.T) {
	f := newCredFixture(t)
	runner := f.cloudRun(f.olga, "run-gl", "https://gitlab.example.com/acme/tools.git")
	for _, tok := range []string{"glpat-one", "glpat-two"} {
		if _, code := f.put(f.olga, map[string]any{"host": "gitlab.example.com", "kind": "https", "token": tok}); code != http.StatusOK {
			t.Fatalf("put %s = %d", tok, code)
		}
		if got, code := f.gitCredential(runner, "run-gl"); code != http.StatusOK || got.Secret != tok {
			t.Fatalf("release after storing %s = %d %+v", tok, code, got)
		}
	}
	if code := f.call("DELETE", "/api/v1/team/git-credentials/gitlab.example.com", f.olga.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if got, code := f.gitCredential(runner, "run-gl"); code != http.StatusNotFound || got.Secret != "" || got.Error != "no_source_credential" {
		t.Fatalf("release after delete = %d %+v; want no credential", code, got)
	}
}

// With an App installation covering the repository, the App token wins even
// when the team also stored a credential for github.com, and that credential
// is neither released nor audited. A repository the App does not cover gets
// the stored credential.
func TestTeamGitCredentialLosesToTheAppWhenTheAppCovers(t *testing.T) {
	f := newCredFixture(t)
	f.connect(f.olga, 501, 7, acmeAdmin)
	if _, code := f.put(f.olga, map[string]any{"host": "github.com", "kind": "https", "token": "github_pat_x"}); code != http.StatusOK {
		t.Fatalf("put = %d", code)
	}
	covered := f.cloudRun(f.olga, "run-widgets", "https://github.com/acme/widgets.git")
	got, code := f.gitCredential(covered, "run-widgets")
	if code != http.StatusOK || got.Kind != "github_app" || got.Secret != "" || !f.app.TokenCovers(got.Token, "acme/widgets") {
		t.Fatalf("covered repository = %d %+v; want the App token", code, got)
	}
	if rels := f.releases(f.olga); len(rels) != 0 {
		t.Fatalf("audit = %+v; the stored credential was released beside the App token", rels)
	}
	uncovered := f.cloudRun(f.olga, "run-else", "https://github.com/someone/else.git")
	got, code = f.gitCredential(uncovered, "run-else")
	if code != http.StatusOK || got.Kind != "https" || got.Secret != "github_pat_x" {
		t.Fatalf("uncovered repository = %d %+v; want the stored credential", code, got)
	}
}

// Only an owner stores, confirms or deletes a credential, and no route ever
// shows its value.
func TestTeamGitCredentialIsOwnerManagedAndWriteOnly(t *testing.T) {
	f := newCredFixture(t)
	owner, editor, reader := teamOf(f.identityFixture)
	for _, who := range []signedIn{editor, reader} {
		if _, code := f.put(who, map[string]any{"host": "gitlab.example.com", "kind": "https", "token": "glpat-x"}); code != http.StatusForbidden {
			t.Fatalf("a non-owner stores a credential: %d", code)
		}
	}
	if _, code := f.put(owner, map[string]any{"host": "gitlab.example.com", "kind": "https", "token": "glpat-secret"}); code != http.StatusOK {
		t.Fatalf("owner put = %d", code)
	}
	if code := f.call("DELETE", "/api/v1/team/git-credentials/gitlab.example.com", editor.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("an editor deletes a credential: %d", code)
	}
	for _, who := range []signedIn{owner, editor, reader} {
		var raw map[string]any
		if code := f.call("GET", "/api/v1/team/git-credentials", who.auth, nil, &raw); code != http.StatusOK {
			t.Fatalf("list = %d", code)
		}
		if blob := jsonText(t, raw); strings.Contains(blob, "glpat-secret") || !strings.Contains(blob, "gitlab.example.com") {
			t.Fatalf("list = %s; want the host and never the token", blob)
		}
	}
	if _, code := f.put(owner, map[string]any{"host": "gitlab.example.com", "kind": "ssh", "private_key": "not a key"}); code != http.StatusBadRequest {
		t.Fatalf("a key that does not parse = %d, want 400", code)
	}
	if _, code := f.put(owner, map[string]any{"host": "https://gitlab.example.com", "kind": "https", "token": "x"}); code != http.StatusBadRequest {
		t.Fatalf("a host with a scheme = %d, want 400", code)
	}
}

func TestTeamGitCredentialNeedsASecretsKey(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	var out struct {
		Error string `json:"error"`
	}
	if code := f.call("POST", "/api/v1/team/git-credentials", olga.auth,
		map[string]any{"host": "gitlab.example.com", "kind": "https", "token": "glpat-x"}, &out); code != http.StatusConflict ||
		out.Error != "secrets_key_required" {
		t.Fatalf("put without a secrets key = %d %+v; want 409 secrets_key_required", code, out)
	}
}

func jsonText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
