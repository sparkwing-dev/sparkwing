package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

var (
	machineScopes = store.MachineTokenScopes()
	memberScopes  = store.CLITokenScopes(store.RoleEditor)
)

type mintedRunner struct {
	Token  string `json:"token"`
	Prefix string `json:"prefix"`
}

func mintRunner(f *identityFixture, auth, name string) mintedRunner {
	f.t.Helper()
	var m mintedRunner
	if code := f.call("POST", "/api/v1/team/tokens", auth, map[string]any{
		"scopes": machineScopes, "name": name, "repos": []string{"github.com/acme/*"},
	}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint %s = %d", name, code)
	}
	return m
}

func runnerAuthenticates(f *identityFixture, m mintedRunner) bool {
	f.t.Helper()
	code := f.call("GET", "/api/v1/triggers/no-such-run", "Bearer "+m.Token, nil, nil)
	switch code {
	case http.StatusNotFound:
		return true
	case http.StatusUnauthorized:
		return false
	}
	f.t.Fatalf("runner probe = %d", code)
	return false
}

type mintedCLI struct {
	Token     string   `json:"token"`
	Prefix    string   `json:"prefix"`
	Family    string   `json:"family"`
	Scopes    []string `json:"scopes"`
	ExpiresAt int64    `json:"expires_at"`
	Profile   string   `json:"profile"`
	Setup     string   `json:"setup"`
	Run       string   `json:"run"`
}

func mintCLI(f *identityFixture, who signedIn, role store.Role) mintedCLI {
	f.t.Helper()
	var m mintedCLI
	if code := f.call("POST", "/api/v1/team/tokens", who.auth, map[string]any{"scopes": store.CLITokenScopes(role)}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint member token = %d", code)
	}
	return m
}

func cliTrigger(f *identityFixture, token string) (int, string) {
	f.t.Helper()
	var out struct {
		RunID string `json:"run_id"`
	}
	code := f.call("POST", "/api/v1/triggers", "Bearer "+token, map[string]any{
		"pipeline": "build",
		"trigger":  map[string]any{"source": "pipeline-trigger@laptop"},
		"git": map[string]any{
			"branch": "main", "sha": strings.Repeat("a", 40),
			"repo_url": "https://github.com/acme/app.git",
		},
	}, &out)
	return code, out.RunID
}

func cliAuthenticates(f *identityFixture, token string) bool {
	f.t.Helper()
	switch code := f.call("GET", "/api/v1/runs/no-such-run", "Bearer "+token, nil, nil); code {
	case http.StatusNotFound:
		return true
	case http.StatusUnauthorized:
		return false
	default:
		f.t.Fatalf("CLI probe = %d", code)
		return false
	}
}

type listedTeamToken struct {
	Prefix    string   `json:"prefix"`
	Name      string   `json:"name"`
	Family    string   `json:"family"`
	Scopes    []string `json:"scopes"`
	CreatedBy string   `json:"created_by"`
	ExpiresAt *int64   `json:"expires_at"`
}

func listTeamTokens(f *identityFixture, who signedIn) map[string]listedTeamToken {
	f.t.Helper()
	var listed []listedTeamToken
	if code := f.call("GET", "/api/v1/team/tokens", who.auth, nil, &listed); code != http.StatusOK {
		f.t.Fatalf("list = %d", code)
	}
	out := map[string]listedTeamToken{}
	for _, tok := range listed {
		out[tok.Prefix] = tok
	}
	return out
}

func TestEditorMemberTokenStartsARunTheTeamSees(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	before := time.Now()
	m := mintCLI(f, editor, store.RoleEditor)

	if m.Family != "member" || slices.Contains(m.Scopes, "team.admin") || !slices.Contains(m.Scopes, "runs.write") {
		t.Fatalf("family %q scopes %v, want the editor's member scopes", m.Family, m.Scopes)
	}
	if !strings.HasPrefix(m.Token, "swt_") {
		t.Fatalf("token %q is not a team token", m.Token)
	}
	if got, want := time.Unix(m.ExpiresAt, 0), before.Add(90*24*time.Hour); got.Before(want.Add(-time.Minute)) || got.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %s, want about %s", got, want)
	}
	if m.Profile != editor.team {
		t.Errorf("profile = %q, want the team slug %q", m.Profile, editor.team)
	}
	for _, want := range []string{"sparkwing cloud connect --controller http", " --name " + editor.team, " --token-stdin"} {
		if !strings.Contains(m.Setup, want) {
			t.Errorf("setup %q lacks %q", m.Setup, want)
		}
	}
	if strings.Contains(m.Setup, m.Token) {
		t.Error("the setup command carries the token, which would land in shell history")
	}
	if m.Run != "sparkwing pipeline trigger <pipeline> --profile "+editor.team {
		t.Errorf("run = %q", m.Run)
	}

	code, runID := cliTrigger(f, m.Token)
	if code != http.StatusAccepted {
		t.Fatalf("trigger with the member token = %d", code)
	}
	var trig struct {
		RepoURL string `json:"repo_url"`
		GitSHA  string `json:"git_sha"`
	}
	if code := f.call("GET", "/api/v1/triggers/"+runID, owner.auth, nil, &trig); code != http.StatusOK {
		t.Fatalf("the owner reads the editor's trigger = %d", code)
	}
	if trig.RepoURL != "https://github.com/acme/app.git" || trig.GitSHA != strings.Repeat("a", 40) {
		t.Errorf("trigger = %+v, want the repository and commit the CLI sent", trig)
	}
}

// A terminal credential never administers the team, and never mints another
// credential, whatever the role of the member it acts for.
func TestMemberTokenCannotAdministerOrMint(t *testing.T) {
	f := newIdentityFixture(t)
	owner, _, _ := teamOf(f)
	m := mintCLI(f, owner, store.RoleOwner)
	if slices.Contains(m.Scopes, "team.admin") {
		t.Fatalf("an owner's member token carries team.admin: %v", m.Scopes)
	}
	bearer := "Bearer " + m.Token
	if code := f.call("PATCH", "/api/v1/team", bearer, map[string]string{"display_name": "x"}, nil); code != http.StatusForbidden {
		t.Errorf("rename team with a member token = %d, want 403", code)
	}
	for _, body := range []map[string]any{
		{"scopes": memberScopes},
		{"scopes": machineScopes, "name": "x", "repos": []string{"github.com/acme/*"}},
	} {
		if code := f.call("POST", "/api/v1/team/tokens", bearer, body, nil); code != http.StatusUnauthorized {
			t.Errorf("mint %v with a member token = %d, want 401", body["scopes"], code)
		}
	}
}

func TestReaderMemberTokenOnlyReads(t *testing.T) {
	f := newIdentityFixture(t)
	_, _, reader := teamOf(f)
	m := mintCLI(f, reader, store.RoleReader)
	if slices.Contains(m.Scopes, "runs.write") || !slices.Contains(m.Scopes, "runs.read") {
		t.Fatalf("scopes = %v, want read-only", m.Scopes)
	}
	if code, _ := cliTrigger(f, m.Token); code != http.StatusForbidden {
		t.Errorf("trigger with a reader's member token = %d, want 403", code)
	}
	if code := f.call("GET", "/api/v1/runs", "Bearer "+m.Token, nil, nil); code != http.StatusOK {
		t.Errorf("list runs with a reader's member token = %d, want 200", code)
	}
}

func TestMintingATeamTokenChecksTheScopeFamilyAndRole(t *testing.T) {
	f := newIdentityFixture(t)
	_, editor, reader := teamOf(f)
	repos := []string{"github.com/acme/*"}
	for _, tc := range []struct {
		name string
		who  signedIn
		body map[string]any
		want int
	}{
		{"no scopes", editor, map[string]any{}, http.StatusBadRequest},
		{"empty scopes", editor, map[string]any{"scopes": []string{}}, http.StatusBadRequest},
		{"unknown scope", editor, map[string]any{"scopes": []string{"runs.read", "made.up"}}, http.StatusBadRequest},
		{"team.admin", editor, map[string]any{"scopes": []string{"team.admin"}}, http.StatusBadRequest},
		{"mixed families", editor, map[string]any{"scopes": []string{"runs.read", "nodes.claim"}, "name": "box", "repos": repos}, http.StatusBadRequest},
		{"machine without a claim scope", editor, map[string]any{"scopes": []string{"runs.state"}, "name": "box", "repos": repos}, http.StatusBadRequest},
		{"member with a name", editor, map[string]any{"scopes": []string{"runs.read"}, "name": "box"}, http.StatusBadRequest},
		{"member above the role", reader, map[string]any{"scopes": []string{"runs.read", "runs.write"}}, http.StatusForbidden},
		{"machine by a reader", reader, map[string]any{"scopes": machineScopes, "name": "box", "repos": repos}, http.StatusForbidden},
		{"member subset", reader, map[string]any{"scopes": []string{"logs.read"}}, http.StatusCreated},
		{"machine subset", editor, map[string]any{"scopes": []string{"triggers.claim"}, "name": "trig", "repos": repos}, http.StatusCreated},
	} {
		var minted struct {
			Scopes  []string `json:"scopes"`
			Command string   `json:"command"`
		}
		if code := f.call("POST", "/api/v1/team/tokens", tc.who.auth, tc.body, &minted); code != tc.want {
			t.Errorf("%s: mint = %d, want %d", tc.name, code, tc.want)
			continue
		}
		if tc.want == http.StatusCreated && !slices.Equal(minted.Scopes, tc.body["scopes"].([]string)) {
			t.Errorf("%s: minted scopes %v, want exactly %v", tc.name, minted.Scopes, tc.body["scopes"])
		}
		if tc.name == "machine subset" && !strings.Contains(minted.Command, " --claim-nodes=false") {
			t.Errorf("a triggers-only machine's command %q still claims nodes", minted.Command)
		}
	}
}

func TestMemberTokenGoesWithTheMembersAccess(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, reader := teamOf(f)
	editorCLI := mintCLI(f, editor, store.RoleEditor)
	readerCLI := mintCLI(f, reader, store.RoleReader)
	ownerCLI := mintCLI(f, owner, store.RoleOwner)

	if code := f.call("PATCH", "/api/v1/team/members/"+editor.id, owner.auth, map[string]string{"role": "reader"}, nil); code != http.StatusNoContent {
		t.Fatalf("demote = %d", code)
	}
	if cliAuthenticates(f, editorCLI.Token) {
		t.Error("a member demoted to reader keeps a member token that writes")
	}
	if code := f.call("DELETE", "/api/v1/team/members/"+reader.id, owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	if cliAuthenticates(f, readerCLI.Token) {
		t.Error("a removed member's member token still authenticates")
	}
	if !cliAuthenticates(f, ownerCLI.Token) {
		t.Error("changing other members revoked the owner's member token")
	}
}

// A member token is personal: its member lists and revokes it, and nobody
// else sees or revokes it, an owner included.
func TestMemberTokensArePersonal(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	mine := mintCLI(f, editor, store.RoleEditor)
	mintCLI(f, owner, store.RoleOwner)

	listed := listTeamTokens(f, editor)
	if got, ok := listed[mine.Prefix]; len(listed) != 1 || !ok || got.ExpiresAt == nil || got.Family != "member" || got.Name != "" {
		t.Fatalf("listed = %+v, want only the editor's member token, with its expiry", listed)
	}
	if code := f.call("DELETE", "/api/v1/team/tokens/"+mine.Prefix, owner.auth, nil, nil); code != http.StatusNotFound {
		t.Errorf("the owner revoking the editor's member token = %d, want 404", code)
	}
	if !cliAuthenticates(f, mine.Token) {
		t.Fatal("another member's revoke reached the token")
	}
	if code := f.call("DELETE", "/api/v1/team/tokens/"+mine.Prefix, editor.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke = %d", code)
	}
	if cliAuthenticates(f, mine.Token) {
		t.Error("a revoked member token still authenticates")
	}
}

func TestMemberTokenMintUsesCommittedMembershipAuthority(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			rawLicense, key := multiTeamLicense(t)
			opts := fixtureOpts{license: rawLicense, key: key}
			if dialect == "postgres" {
				opts.store = openPostgresStore(t)
			}
			f := newIdentityFixtureWith(t, opts)
			owner, editor, reader := teamOf(f)
			for who, role := range map[signedIn]store.Role{owner: store.RoleOwner, editor: store.RoleEditor, reader: store.RoleReader} {
				token := mintCLI(f, who, role)
				if slices.Contains(token.Scopes, controller.ScopeTeamAdmin) {
					t.Fatal("member token carries team administration")
				}
				if got := slices.Contains(token.Scopes, controller.ScopeRunsWrite); got != (who.id != reader.id) {
					t.Fatalf("ordinary member authority mismatch: write=%v", got)
				}
			}
			stale := store.CLITokenScopes(store.RoleEditor)
			original := slices.Clone(stale)
			if code := f.call("PATCH", "/api/v1/team/members/"+editor.id, owner.auth, map[string]string{"role": "reader"}, nil); code != http.StatusNoContent {
				t.Fatalf("demote=%d", code)
			}
			tenant, err := f.store.ForTeam(context.Background(), store.Team(editor.team))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := tenant.CreateMemberToken(context.Background(), "pending-editor", stale, editor.id, time.Now()); err == nil {
				t.Fatal("a demoted member minted the editor's scopes")
			}
			if !slices.Equal(stale, original) {
				t.Fatal("mint mutated caller scopes")
			}
			raw, tok, err := tenant.CreateMemberToken(context.Background(), "reader-subset", []string{controller.ScopeLogsRead}, editor.id, time.Now())
			if err != nil || !slices.Equal(tok.Scopes, []string{controller.ScopeLogsRead}) {
				t.Fatalf("requested subset widened: %v error=%v", tok, err)
			}
			if code := f.call("DELETE", "/api/v1/team/members/"+editor.id, owner.auth, nil, nil); code != http.StatusNoContent {
				t.Fatalf("remove=%d", code)
			}
			if raw, tok, err := tenant.CreateMemberToken(context.Background(), "pending-removed", []string{controller.ScopeRunsRead}, editor.id, time.Now()); err == nil || raw != "" || tok != nil {
				t.Fatal("removed member minted a token")
			}
			if code := f.call("GET", "/api/v1/runs", "Bearer "+raw, nil, nil); code != http.StatusUnauthorized {
				t.Errorf("removed member bearer=%d", code)
			}
		})
	}
}

// A machine token carries secrets.read and outlives a session, so the machine
// a removed member connected stops being the team's in the same step.
func TestRemovingAMemberRevokesTheMachineTokensTheyMinted(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	editorRunner := mintRunner(f, editor.auth, "eddie")
	ownerRunner := mintRunner(f, owner.auth, "olga")
	if !runnerAuthenticates(f, editorRunner) {
		t.Fatal("the editor's fresh machine token does not authenticate")
	}
	if code := f.call("DELETE", "/api/v1/team/members/"+editor.id, owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	if runnerAuthenticates(f, editorRunner) {
		t.Error("a removed member's machine token still authenticates")
	}
	if !runnerAuthenticates(f, ownerRunner) {
		t.Error("removing the editor revoked the owner's machine token")
	}
}

// A reader may not mint a machine token, so a demotion to reader takes back
// the ones they already hold.
func TestDemotingAMemberToReaderRevokesTheirMachineTokens(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	editorRunner := mintRunner(f, editor.auth, "eddie")
	ownerRunner := mintRunner(f, owner.auth, "olga")
	if code := f.call("PATCH", "/api/v1/team/members/"+editor.id, owner.auth, map[string]string{"role": "reader"}, nil); code != http.StatusNoContent {
		t.Fatalf("demote = %d", code)
	}
	if runnerAuthenticates(f, editorRunner) {
		t.Error("a member demoted to reader keeps a working machine token")
	}
	if !runnerAuthenticates(f, ownerRunner) {
		t.Error("demoting the editor revoked the owner's machine token")
	}
}

func TestMachineTokensExpireAndTheListShowsWhen(t *testing.T) {
	f := newIdentityFixture(t)
	owner, _, _ := teamOf(f)
	before := time.Now()
	m := mintRunner(f, owner.auth, "olga")
	got, ok := listTeamTokens(f, owner)[m.Prefix]
	if !ok || got.ExpiresAt == nil || got.Family != "machine" || got.Name != "olga" || !slices.Equal(got.Scopes, machineScopes) {
		t.Fatalf("listed = %+v, want the machine token olga with an expiry", got)
	}
	at := time.Unix(*got.ExpiresAt, 0)
	want := before.Add(90 * 24 * time.Hour)
	if at.Before(want.Add(-time.Minute)) || at.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %s, want about %s", at, want)
	}
}

// The advertised command is what a person pastes into a shell, so it carries
// the repositories the machine may build: a runner started from it refuses
// every other one.
func TestMintedMachineCommandCarriesTheRepositoryAllowlist(t *testing.T) {
	f := newIdentityFixture(t)
	alice := f.user("alice", "alice@example.com")
	var minted struct {
		Family  string `json:"family"`
		Name    string `json:"name"`
		Command string `json:"command"`
	}
	if code := f.call("POST", "/api/v1/team/tokens", alice.auth, map[string]any{
		"scopes": machineScopes, "name": "alice-laptop", "repos": []string{"GitHub.com/acme/*", "github.com/other/app"},
	}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	if minted.Family != "machine" || minted.Name != "alice-laptop" {
		t.Errorf("minted family %q name %q", minted.Family, minted.Name)
	}
	for _, want := range []string{" --allow-repo 'github.com/acme/*'", " --allow-repo 'github.com/other/app'", " --also-claim-triggers"} {
		if !strings.Contains(minted.Command, want) {
			t.Fatalf("command %q lacks %q", minted.Command, want)
		}
	}
}

func TestMintingAMachineTokenRequiresValidRepositories(t *testing.T) {
	f := newIdentityFixture(t)
	alice := f.user("alice", "alice@example.com")
	for name, repos := range map[string]any{
		"absent":    nil,
		"empty":     []string{},
		"scheme":    []string{"https://github.com/acme/*"},
		"host only": []string{"github.com"},
		"shell":     []string{"github.com/acme/app';id;'"},
		"wild host": []string{"*/acme/app"},
	} {
		body := map[string]any{"scopes": machineScopes, "name": "box"}
		if repos != nil {
			body["repos"] = repos
		}
		if code := f.call("POST", "/api/v1/team/tokens", alice.auth, body, nil); code != http.StatusBadRequest {
			t.Errorf("%s: mint = %d, want 400", name, code)
		}
	}
}

// CLI and runner tokens minted before the team kind keep authenticating, and
// the team-token routes list and revoke them by the family they act as.
func TestLegacyCLIAndRunnerTokensStayListedAndRevocable(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, reader := teamOf(f)
	tenant, err := f.store.ForTeam(context.Background(), store.Team(owner.team))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cliRaw, cli, err := tenant.CreateTokenWith(context.Background(), "eddie@example.com", store.TokenKindUser,
		store.CLITokenScopes(store.RoleEditor), store.CLITokenLifetime, now, store.TokenOptions{CreatedBy: editor.id})
	if err != nil {
		t.Fatal(err)
	}
	runnerRaw, runner, err := tenant.CreateTokenWith(context.Background(), "agent:old-box", store.TokenKindRunner,
		machineScopes, store.RunnerTokenLifetime, now, store.TokenOptions{CreatedBy: editor.id})
	if err != nil {
		t.Fatal(err)
	}
	legacyRunner := mintedRunner{Token: runnerRaw, Prefix: runner.Prefix}
	if !cliAuthenticates(f, cliRaw) || !runnerAuthenticates(f, legacyRunner) {
		t.Fatal("a legacy CLI or runner token stopped authenticating")
	}
	if code, _ := cliTrigger(f, cliRaw); code != http.StatusAccepted {
		t.Errorf("trigger with a legacy CLI token = %d, want 202", code)
	}
	mine := listTeamTokens(f, editor)
	if mine[cli.Prefix].Family != "member" || mine[runner.Prefix].Family != "machine" || mine[runner.Prefix].Name != "old-box" {
		t.Fatalf("the editor's list = %+v, want the legacy CLI token as member and the runner as machine old-box", mine)
	}
	ownerView := listTeamTokens(f, owner)
	if _, ok := ownerView[cli.Prefix]; ok {
		t.Error("the owner sees the editor's legacy CLI token")
	}
	if _, ok := ownerView[runner.Prefix]; !ok {
		t.Error("the owner does not see the team's legacy runner token")
	}
	if _, ok := listTeamTokens(f, reader)[runner.Prefix]; ok {
		t.Error("a reader sees another member's machine token")
	}
	if code := f.call("DELETE", "/api/v1/team/tokens/"+runner.Prefix, reader.auth, nil, nil); code != http.StatusNotFound {
		t.Errorf("a reader revoking a machine token = %d, want 404", code)
	}
	if code := f.call("DELETE", "/api/v1/team/tokens/"+cli.Prefix, editor.auth, nil, nil); code != http.StatusNoContent {
		t.Errorf("revoking one's own legacy CLI token = %d, want 204", code)
	}
	if code := f.call("DELETE", "/api/v1/team/tokens/"+runner.Prefix, owner.auth, nil, nil); code != http.StatusNoContent {
		t.Errorf("an owner revoking a legacy runner token = %d, want 204", code)
	}
	if cliAuthenticates(f, cliRaw) || runnerAuthenticates(f, legacyRunner) {
		t.Error("a revoked legacy token still authenticates")
	}
}

// An executor enrollment binds a machine credential; a machine team token is
// one and a member team token is not, whatever scopes it carries.
func TestEnrollmentAcceptsAMachineTeamTokenOnly(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	adminRaw, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	_, machine, err := st.CreateToken("agent:desk", store.TokenKindTeam, []string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	_, member, err := st.CreateToken("eddie@example.com", store.TokenKindTeam, []string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()
	enroll := func(name, prefix string) int {
		status, _ := agentRequest(t, http.MethodPut, srv.URL+"/api/v1/agents/"+name, adminRaw, map[string]any{
			"token_prefix": prefix, "kind": "agent", "location": "local", "max_concurrent": 1,
		})
		return status
	}
	if status := enroll("desk", machine.Prefix); status != http.StatusNoContent {
		t.Errorf("enrolling a machine team token = %d, want 204", status)
	}
	if status := enroll("laptop", member.Prefix); status != http.StatusBadRequest {
		t.Errorf("enrolling a member team token = %d, want 400", status)
	}
}
