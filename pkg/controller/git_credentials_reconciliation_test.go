package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type gitReleaseFixture struct {
	t                    *testing.T
	s                    *Server
	session, csrf, claim string
}

func newGitReleaseFixture(t *testing.T) *gitReleaseFixture {
	t.Helper()
	s, session, csrf := reviewAccount(t, "owner")
	e := &EngineController{Store: s.store}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Source.Location.RemoteURL = "https://gitlab.example.com/acme/tools.git"
	run.Request.Request.Git.Repository.Key = runspec.RepoKey("git:gitlab.example.com/acme/tools")
	encoded, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE runs SET repo_key='git:gitlab.example.com/acme/tools',request_json=? WHERE team='a' AND id='run'`, encoded)
	a := engineRuntimeClaim(t, e, capability.UnitNode)
	s.store.SetClaimProfileResolver(engineProfileFake{})
	engineSQL(t, s.store, `UPDATE capabilities SET created_by='member' WHERE prefix='runner'`)
	c := a.Principal.Capability
	issued, err := s.store.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: a.Principal.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.SourceRead}, Binding: c.Binding, Repositories: c.Repositories, Data: c.Data, ExpiresAt: c.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	return &gitReleaseFixture{t: t, s: s, session: session, csrf: csrf, claim: issued.Raw}
}

func (f *gitReleaseFixture) request(method, path, body string, status int) *httptest.ResponseRecorder {
	f.t.Helper()
	return reviewSessionRequest(f.t, f.s, f.session, f.csrf, method, path, body, status)
}

func (f *gitReleaseFixture) put(token string) {
	f.t.Helper()
	w := f.request("POST", "/api/v1/team/git-credentials", `{"host":"gitlab.example.com","kind":"https","token":"`+token+`"}`, 200)
	if strings.Contains(w.Body.String(), token) {
		f.t.Fatal("write response disclosed credential")
	}
}

func (f *gitReleaseFixture) consent(enabled bool) {
	f.t.Helper()
	body := `{"enabled":false}`
	if enabled {
		body = `{"enabled":true}`
	}
	f.request("PUT", "/api/v1/tokens/runner/git-credentials", body, 200)
	w := f.request("GET", "/api/v1/tokens/runner", "", 200)
	var metadata capability.Metadata
	if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil || metadata.Prefix != "runner" || metadata.GitCredentials != enabled {
		f.t.Fatalf("machine consent metadata=%s: %v", w.Body, err)
	}
}

func (f *gitReleaseFixture) release(status int) SourceCredentialResponse {
	f.t.Helper()
	w := engineClaimHTTP(f.t, f.s.Handler(), f.claim, "POST", "/api/v1/runs/run/source-credential", "", status)
	if w.Header().Get("Cache-Control") != "no-store" {
		f.t.Fatal("source response is cacheable")
	}
	var got SourceCredentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		f.t.Fatal(err)
	}
	if status != 200 && got.Secret != "" {
		f.t.Fatal("refusal released source credential")
	}
	return got
}

func (f *gitReleaseFixture) audits(want int) {
	f.t.Helper()
	w := f.request("GET", "/api/v1/team/git-credentials/releases", "", 200)
	var got struct {
		Releases []GitCredentialReleaseJSON `json:"releases"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		f.t.Fatal(err)
	}
	if len(got.Releases) != want {
		f.t.Fatalf("audits=%s, want %d", w.Body, want)
	}
	for _, row := range got.Releases {
		if row.RunID != "run" || row.JobID != "job" || row.Generation != 1 || row.Host != "gitlab.example.com" || row.Runner != "runner" || row.CapabilityPrefix == "" || row.Repository != "git:gitlab.example.com/acme/tools" {
			f.t.Fatalf("unbound audit: %+v", row)
		}
	}
}

func TestTeamGitCredentialSSHIsUnusableUntilConfirmed(t *testing.T) {
	f := newGitReleaseFixture(t)
	f.consent(true)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	f.s.hostKeyScan = func(_ context.Context, name string, port int) (ssh.PublicKey, error) {
		if name != "gitlab.example.com" || port != 22 {
			t.Fatalf("host scan %s:%d", name, port)
		}
		return host, nil
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(block))
	body, err := json.Marshal(map[string]string{"host": "GitLab.Example.com", "kind": "ssh", "private_key": key})
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("POST", "/api/v1/team/git-credentials", string(body), 200)
	var stored GitCredentialJSON
	if err := json.Unmarshal(w.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	want := ssh.FingerprintSHA256(host)
	if stored.Host != "gitlab.example.com" || stored.Fingerprint != want || stored.Confirmed || !strings.HasPrefix(stored.PublicKey, "ssh-ed25519 ") || !strings.Contains(stored.HostKey, "gitlab.example.com ssh-ed25519 ") || strings.Contains(w.Body.String(), key) {
		t.Fatalf("stored=%s", w.Body)
	}
	f.release(403)
	f.audits(0)
	f.request("POST", "/api/v1/team/git-credentials/gitlab.example.com/confirm", `{"fingerprint":"SHA256:wrong"}`, 409)
	f.release(403)
	f.request("POST", "/api/v1/team/git-credentials/gitlab.example.com/confirm", `{"fingerprint":"`+want+`"}`, 200)
	got := f.release(200)
	if got.Kind != "ssh" || got.Secret != key || got.Host != stored.Host || !strings.Contains(got.KnownHosts, "gitlab.example.com ssh-ed25519 ") {
		t.Fatal("confirmed source did not retain key and pin")
	}
	f.audits(1)
}

func TestTeamGitCredentialGoesToLocalMachinesOnlyWhenOptedIn(t *testing.T) {
	f := newGitReleaseFixture(t)
	f.put("private-git-token")
	f.release(403)
	f.audits(0)
	f.consent(true)
	if got := f.release(200); got.Secret != "private-git-token" || got.Kind != "https" || got.Username != "x-access-token" {
		t.Fatal("opted-in source did not receive credential")
	}
	w := f.request("POST", "/api/v1/runners", `{"name":"idle","repos":["gitlab.example.com/acme/*"]}`, 201)
	var idle credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &idle); err != nil {
		t.Fatal(err)
	}
	w = engineClaimHTTP(t, f.s.Handler(), idle.Capability, "POST", "/api/v1/runs/run/source-credential", "", 403)
	if strings.Contains(w.Body.String(), "private-git-token") {
		t.Fatal("an idle runner received a source credential")
	}
	f.consent(false)
	f.release(403)
	f.audits(1)
}

func TestTeamGitCredentialCloudNeedsNoMachineConsent(t *testing.T) {
	s, session, csrf := reviewAccount(t, "owner")
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Source.Location.RemoteURL = "https://gitlab.example.com/acme/tools.git"
	run.Request.Request.Git.Repository.Key = runspec.RepoKey("git:gitlab.example.com/acme/tools")
	encoded, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE runs SET dispatch='controller',repo_key='git:gitlab.example.com/acme/tools',request_json=? WHERE team='a' AND id='run'`, encoded)
	engineSQL(t, s.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	raw := LauncherFixtureClaim(t, s.store, "run", "job")
	s.store.SetClaimProfileResolver(engineEnrollmentFunc(func(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
		return match.Profile{Location: "cloud", Class: "cloud", Capacity: match.Resources{Cores: 1}}, match.Resources{Cores: 1}, nil
	}))
	f := &gitReleaseFixture{t: t, s: s, session: session, csrf: csrf, claim: raw}
	f.put("private-cloud-git")
	if got := f.release(200); got.Secret != "private-cloud-git" {
		t.Fatal("Cloud claim did not receive its team's credential")
	}
	var consents int
	if err := s.store.DB().QueryRow(`SELECT COUNT(*) FROM git_credential_machines`).Scan(&consents); err != nil || consents != 0 {
		t.Fatalf("Cloud release required machine consent: %d: %v", consents, err)
	}
}

func TestTeamGitCredentialRotationAndRevocation(t *testing.T) {
	f := newGitReleaseFixture(t)
	f.consent(true)
	for _, token := range []string{"private-git-one", "private-git-two"} {
		f.put(token)
		if got := f.release(200); got.Secret != token {
			t.Fatal("rotation did not change released value")
		}
	}
	f.request("DELETE", "/api/v1/team/git-credentials/gitlab.example.com", "", 204)
	f.release(404)
	f.audits(2)
}

func TestTeamGitCredentialConsentRequiresTheMachineOwnerProperty(t *testing.T) {
	property := func(role uint8, enabled bool) bool {
		f := newGitReleaseFixture(t)
		f.put("private-git-consent")
		f.consent(true)
		engineSQL(t, f.s.store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('other','other@example.test',1,1)`)
		engineSQL(t, f.s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','other',?,1)`, []string{"owner", "editor", "reader"}[role%3])
		raw, csrf, _, err := f.s.store.CreateAccountSession(t.Context(), store.Account{ID: "other"}, "a", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		body := `{"enabled":false}`
		if enabled {
			body = `{"enabled":true}`
		}
		reviewSessionRequest(t, f.s, raw, csrf, "PUT", "/api/v1/tokens/runner/git-credentials", body, 403)
		var owner string
		if err := f.s.store.DB().QueryRow(`SELECT enabled_by FROM git_credential_machines WHERE team='a' AND capability_prefix='runner'`).Scan(&owner); err != nil || owner != "member" {
			t.Fatalf("another account changed machine consent: %q: %v", owner, err)
		}
		if f.release(200).Secret != "private-git-consent" {
			t.Fatal("refused consent update changed source release")
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 18}); err != nil {
		t.Fatal(err)
	}
}

func TestTeamGitCredentialIsOwnerManagedAndWriteOnly(t *testing.T) {
	f := newGitReleaseFixture(t)
	f.put("private-git-secret")
	for _, role := range []string{"owner", "editor", "reader"} {
		engineSQL(t, f.s.store, `UPDATE memberships SET role=? WHERE team='a' AND account_id='member'`, role)
		w := f.request("GET", "/api/v1/team/git-credentials", "", 200)
		if strings.Contains(w.Body.String(), "private-git-secret") || !strings.Contains(w.Body.String(), "gitlab.example.com") {
			t.Fatalf("credential listing=%s", w.Body)
		}
		if role != "owner" {
			f.request("POST", "/api/v1/team/git-credentials", `{"host":"gitlab.example.com","kind":"https","token":"changed"}`, 403)
			f.request("POST", "/api/v1/team/git-credentials/gitlab.example.com/confirm", `{"fingerprint":"wrong"}`, 403)
			f.request("DELETE", "/api/v1/team/git-credentials/gitlab.example.com", "", 403)
		}
	}
	engineSQL(t, f.s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='member'`)
	f.request("POST", "/api/v1/team/git-credentials", `{"host":"gitlab.example.com","kind":"ssh","private_key":"not a key"}`, 400)
	f.request("POST", "/api/v1/team/git-credentials", `{"host":"https://gitlab.example.com","kind":"https","token":"x"}`, 400)
	f.consent(true)
	if got := f.release(200); got.Secret != "private-git-secret" {
		t.Fatal("refused mutation changed credential")
	}
}

func TestTeamGitCredentialNeedsASecretsKey(t *testing.T) {
	f := newGitReleaseFixture(t)
	w := f.request("POST", "/api/v1/tokens", `{"grants":["team.admin"]}`, 201)
	var member credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &member); err != nil {
		t.Fatal(err)
	}
	f.s.WithSecretsCipher(nil)
	w = engineClaimHTTP(t, f.s.Handler(), member.Capability, "POST", "/api/v1/team/git-credentials", `{"host":"gitlab.example.com","kind":"https","token":"private-token"}`, 503)
	var failure apierr.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Error.Code != apierr.Unavailable || strings.Contains(w.Body.String(), "private-token") {
		t.Fatalf("missing key=%s: %v", w.Body, err)
	}
	var count int
	if err := f.s.store.DB().QueryRow(`SELECT COUNT(*) FROM git_credentials`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused credential persisted: %d: %v", count, err)
	}
}

func TestTeamGitCredentialStaysWithTheTeamClaimAndHost(t *testing.T) {
	property := func(noise uint32, mode uint8) bool {
		f := newGitReleaseFixture(t)
		f.put("private-git-token")
		f.consent(true)
		if f.release(200).Secret != "private-git-token" {
			t.Fatal("control did not release credential")
		}
		tenant, err := f.s.store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/runs/run/source-credential"
		status := 403
		switch mode % 5 {
		case 0:
			if err := f.s.store.AsOperator().CreateTeam(t.Context(), "foreign"); err != nil {
				t.Fatal(err)
			}
			if err := teststore.SeedRun(f.s.store, t.Context(), "foreign", store.Run{ID: "foreign-run", Pipeline: "foreign-private", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			path = "/api/v1/runs/foreign-run/source-credential"
		case 1:
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			run.Request.Request.Source.Location.RemoteURL = "https://gitlab.other.example/acme/tools.git"
			encoded, err := json.Marshal(run.Request)
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, f.s.store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, encoded)
			status = 404
		case 2:
			status = 401
			engineSQL(t, f.s.store, `UPDATE nodes SET generation=generation+? WHERE team='a' AND run_id='run' AND node_id='job'`, int64(noise)+1)
		case 3:
			root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "runner"}
			if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: root, CapabilityPrefix: root.ID}, root); err != nil {
				t.Fatal(err)
			}
			status = 401
		case 4:
			if err := f.s.store.AsOperator().CreateTeam(t.Context(), "foreign"); err != nil {
				t.Fatal(err)
			}
			engineSQL(t, f.s.store, `UPDATE git_credentials SET team='foreign' WHERE team='a'`)
			status = 404
		}
		w := engineClaimHTTP(t, f.s.Handler(), f.claim, "POST", path, "", status)
		if strings.Contains(w.Body.String(), "private-git-token") || strings.Contains(w.Body.String(), "foreign-private") {
			t.Fatal("refusal disclosed private source facts")
		}
		f.audits(1)
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Fatal(err)
	}
}
