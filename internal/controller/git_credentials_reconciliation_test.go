package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"golang.org/x/crypto/ssh"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type gitReleaseFixture struct {
	t                    *testing.T
	s                    *serverFixture
	session, csrf, claim string
}

func newGitReleaseFixture(t *testing.T) *gitReleaseFixture {
	t.Helper()
	s, session, csrf := reviewAccount(t, "owner")
	e := newEngineFixture(s.store, EngineController{})
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
	s.engine.claimProfiles = engineProfileFake{}
	engineSQL(t, s.store, `UPDATE capabilities SET created_by='member' WHERE prefix='runner'`)
	c := a.Principal.Capability
	issued, err := s.store.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: a.Principal.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.SourceRead}, Binding: c.Binding, Repositories: c.Repositories, Data: c.Data, ExpiresAt: c.ExpiresAt})
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
	w := f.request("POST", "/api/v1/team/git-credentials", `{"host_wide":true, "host":"gitlab.example.com","kind":"https","token":"`+token+`"}`, 200)
	if strings.Contains(w.Body.String(), token) {
		f.t.Fatal("write response disclosed credential")
	}
}

func (f *gitReleaseFixture) registerRunner(personal bool) {
	f.t.Helper()
	registerFixtureRunner(f.t, f.s.store, false)
	if personal {
		w := f.request("POST", "/api/v1/runners", `{"name":"personal-source","repos":["gitlab.example.com/acme/*"],"personal":true}`, 201)
		var issued credentialResponse
		if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
			f.t.Fatal(err)
		}
		var requirement string
		if err := teststore.SQL(f.t, f.s.store).QueryRow(`SELECT json_extract(authority_json,'$.origin.requirement') FROM capabilities WHERE prefix=?`, issued.Prefix).Scan(&requirement); err != nil {
			f.t.Fatal(err)
		}
		engineSQL(f.t, f.s.store, `UPDATE capabilities SET authority_json=CAST(json_set(authority_json,'$.origin.personal',json('true'),'$.origin.requirement',json(?)) AS BLOB) WHERE prefix='runner'`, requirement)
	}
}

func registerFixtureRunner(t *testing.T, s *store.Store, personal bool) {
	t.Helper()
	var body []byte
	if err := teststore.SQL(t, s).QueryRow(`SELECT authority_json FROM capabilities WHERE prefix='runner'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var record store.CapabilityRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record.Origin = &store.CapabilityOrigin{Personal: personal, Kind: capability.ProvisionMachine, RegistrationID: "runner:a:runner"}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s, `UPDATE capabilities SET authority_json=? WHERE prefix='runner'`, body)
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
		Releases []gitCredentialReleaseJSON `json:"releases"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		f.t.Fatal(err)
	}
	if len(got.Releases) != want {
		f.t.Fatalf("audits=%s, want %d", w.Body, want)
	}
	for _, row := range got.Releases {
		if row.RunID != "run" || row.JobID != fmt.Sprintf("%x", sha256.Sum256([]byte("job"))) || row.Generation != 1 || row.Host != "" || row.Runner != fmt.Sprintf("%x", sha256.Sum256([]byte("runner"))) || len(row.CapabilityPrefix) != sha256.Size*2 || row.Repository != fmt.Sprintf("%x", sha256.Sum256([]byte("git:gitlab.example.com/acme/tools"))) {
			f.t.Fatalf("unbound audit: %+v", row)
		}
	}
}

func TestTeamGitCredentialSSHIsUnusableUntilConfirmed(t *testing.T) {
	f := newGitReleaseFixture(t)
	f.registerRunner(false)
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
	body, err := json.Marshal(map[string]any{"host_wide": true, "host": "GitLab.Example.com", "kind": "ssh", "private_key": key})
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
	refused := engineClaimHTTP(t, f.s.Handler(), f.claim, "POST", "/api/v1/runs/run/source-credential", "", 403)
	if !strings.Contains(refused.Body.String(), `"reason":"credential_unconfirmed"`) {
		t.Fatalf("unconfirmed host reason: %s", refused.Body)
	}
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

func TestTeamGitCredentialGoesToSharedRunnersNeverPersonalMachines(t *testing.T) {
	f := newGitReleaseFixture(t)
	f.put("private-git-token")
	f.release(403)
	f.registerRunner(true)
	refused := engineClaimHTTP(t, f.s.Handler(), f.claim, "POST", "/api/v1/runs/run/source-credential", "", 403)
	if !strings.Contains(refused.Body.String(), `"reason":"personal_machine"`) || strings.Contains(refused.Body.String(), "private-git-token") {
		t.Fatalf("personal machine refusal: %s", refused.Body)
	}
	f.audits(0)
	f.registerRunner(false)
	if got := f.release(200); got.Secret != "private-git-token" || got.Kind != "https" || got.Username != "x-access-token" || got.Host != "gitlab.example.com" {
		t.Fatal("shared runner did not receive credential")
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
	f.audits(1)
}

func TestTeamGitCredentialReachesCloudWork(t *testing.T) {
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
	s.engine.claimProfiles = engineEnrollmentFunc(func(context.Context, capability.Principal) (match.Profile, error) {
		return match.Profile{Location: "cloud", Class: "cloud"}, nil
	})
	f := &gitReleaseFixture{t: t, s: s, session: session, csrf: csrf, claim: raw}
	f.put("private-cloud-git")
	if got := f.release(200); got.Secret != "private-cloud-git" || got.Host != "gitlab.example.com" {
		t.Fatal("Cloud claim did not receive its team's credential")
	}
}

func TestTeamGitCredentialRotationAndRevocation(t *testing.T) {
	f := newGitReleaseFixture(t)
	f.registerRunner(false)
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
			f.request("POST", "/api/v1/team/git-credentials", `{"host_wide":true, "host":"gitlab.example.com","kind":"https","token":"changed"}`, 403)
			f.request("POST", "/api/v1/team/git-credentials/gitlab.example.com/confirm", `{"fingerprint":"wrong"}`, 403)
			f.request("DELETE", "/api/v1/team/git-credentials/gitlab.example.com", "", 403)
		}
	}
	engineSQL(t, f.s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='member'`)
	f.request("POST", "/api/v1/team/git-credentials", `{"host_wide":true, "host":"gitlab.example.com","kind":"ssh","private_key":"not a key"}`, 400)
	f.request("POST", "/api/v1/team/git-credentials", `{"host_wide":true, "host":"https://gitlab.example.com","kind":"https","token":"x"}`, 400)
	f.registerRunner(false)
	if got := f.release(200); got.Secret != "private-git-secret" {
		t.Fatal("refused mutation changed credential")
	}
}

func TestTeamGitCredentialNeedsASecretsKey(t *testing.T) {
	f := newGitReleaseFixture(t)
	w := f.request("POST", "/api/v1/tokens", `{"grants":["team.admin"],"allow_team_admin":true}`, 201)
	var member credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &member); err != nil {
		t.Fatal(err)
	}
	f.s.WithSecretsCipher(nil)
	w = engineClaimHTTP(t, f.s.Handler(), member.Capability, "POST", "/api/v1/team/git-credentials", `{"host_wide":true, "host":"gitlab.example.com","kind":"https","token":"private-token"}`, 503)
	var failure apierr.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Error.Code != apierr.Unavailable || strings.Contains(w.Body.String(), "private-token") {
		t.Fatalf("missing key=%s: %v", w.Body, err)
	}
	var count int
	if err := teststore.SQL(t, f.s.store).QueryRow(`SELECT COUNT(*) FROM git_credentials`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused credential persisted: %d: %v", count, err)
	}
}

func TestTeamGitCredentialStaysWithTheTeamClaimAndHost(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 5.1s measured in the short class; the full suite runs it")
	}
	property := func(noise uint32, mode uint8) bool {
		f := newGitReleaseFixture(t)
		f.put("private-git-token")
		f.registerRunner(false)
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
			teststore.SeedTeam(t, f.s.store, "foreign")
			if err := teststore.SeedRun(f.s.store, t.Context(), "foreign", store.Run{RunSummary: runstate.RunSummary{ID: "foreign-run", Pipeline: "foreign-private", Status: "running"}}); err != nil {
				t.Fatal(err)
			}
			path = "/api/v1/runs/foreign-run/source-credential"
			status = 404
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
			revokeEngineMachine(t, f.s.store, "runner")
			status = 401
		case 4:
			teststore.SeedTeam(t, f.s.store, "foreign")
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
