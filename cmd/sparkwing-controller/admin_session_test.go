package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func adminSessionFixture(t *testing.T) (*store.Store, *secrets.Cipher, string) {
	t.Helper()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	st := openState(t)
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	credentialsDir, epochDir := t.TempDir(), t.TempDir()
	epoch, err := os.ReadFile(filepath.Join(p.Root, "credential-epoch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epochDir, "credential-epoch"), epoch, 0o600); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{42}, secrets.KeySize)
	if err := os.WriteFile(filepath.Join(credentialsDir, "secrets-key"), []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(deploymentRecordPath(p.StateDB()))
	if err != nil {
		t.Fatal(err)
	}
	var rec deploymentRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatal(err)
	}
	rec.CredentialsDir, rec.EpochDir, rec.Kind = credentialsDir, epochDir, "team"
	body, err = json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deploymentRecordPath(p.StateDB()), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p.Root, "credential-epoch")); err != nil {
		t.Fatal(err)
	}
	return st, cipher, epochDir
}

func TestAdminSessionEnrollsSharedRunnerAndReservedLauncher(t *testing.T) {
	st, cipher, epochDir := adminSessionFixture(t)
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','ops-admin','owner',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var session, audit bytes.Buffer
	if err := runAdminFixture([]string{"session", "--team", "default", "--reason", "enroll build machines", "--output", "plain"}, &session, &audit); err != nil {
		t.Fatal(err)
	}
	raw := strings.TrimSpace(session.String())
	if raw == "" || strings.Contains(audit.String(), raw) || !strings.Contains(audit.String(), `"principal_id":"ops-admin"`) || !strings.Contains(audit.String(), `"command":"sparkwing-controller admin session"`) {
		t.Fatalf("session missing or audit invalid: %s", &audit)
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := credentials.LoadEpoch(epochDir)
	if err != nil {
		t.Fatal(err)
	}
	var server *controller.Server
	st, err = store.Open(p.StateDB(), store.WithCredentialEpoch(epoch), store.WithPolicies(func(s *store.Store) (store.Policies, error) {
		server = controller.NewDeployment(s, deploymenttest.Team(t), nil).WithSecretsCipher(cipher)
		return server.StorePolicies(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	tenant, err := st.ForTeam(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	events, err := tenant.BusinessEvents(t.Context(), "session.break_glass", 100, "")
	if err != nil || len(events) != 1 {
		t.Fatalf("session mint missing durable audit: %+v %v", events, err)
	}
	if event := events[0]; event.Account != "ops-admin" || event.Actor != "sparkwing-controller admin" || event.Team != "default" || len(event.Attrs) != 0 {
		t.Fatalf("session mint audit identity or privacy: %+v", event)
	}
	httpServer := httptest.NewTLSServer(server.Handler())
	defer httpServer.Close()
	manager, err := client.NewSessionRunnerTokenManager(t.Context(), client.EngineOptions{URL: httpServer.URL, HTTP: httpServer.Client()}, raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, launcher := range []bool{false, true} {
		request := controllerwire.EnrollRunnerRequest{Name: "build", Repos: []string{"github.com/acme/repo"}, Platform: "kubernetes"}
		if launcher {
			request.Name = "deploy"
			request.ReservedPools = []string{"deploy"}
			request.Grants = []string{string(capability.ClaimsLaunch)}
		}
		issued, err := manager.EnrollRunner(t.Context(), request)
		if err != nil || issued == nil || issued.Capability == "" {
			t.Fatalf("launcher=%v enrollment: %v", launcher, err)
		}
		if launcher && !slices.Equal(issued.Grants, []string{string(capability.ClaimsLaunch)}) {
			t.Fatalf("launcher grants: %v", issued.Grants)
		}
	}
	session.Reset()
	audit.Reset()
	if err := runAdminFixture([]string{"session", "--team", "default", "--reason", "enroll another machine", "--output", "json"}, &session, &audit); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(session.Bytes(), &result); err != nil || result.Session == "" || strings.Contains(audit.String(), result.Session) {
		t.Fatal("explicit JSON session output was missing or leaked to audit")
	}
	events, err = tenant.BusinessEvents(t.Context(), "session.break_glass", 100, "")
	if err != nil || len(events) != 2 {
		t.Fatalf("repeat session mint lost audit: %+v %v", events, err)
	}
}

func TestAdminSessionRequiresExistingTeamAdministrator(t *testing.T) {
	st, _, _ := adminSessionFixture(t)
	defer func() { _ = st.Close() }()
	for _, role := range []string{"reader", "editor", ""} {
		if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `DELETE FROM memberships WHERE account_id='ops-admin'`); err != nil {
			t.Fatal(err)
		}
		if role != "" {
			if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','ops-admin',?,1)`, role); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		err := runAdminFixture([]string{"session", "--team", "default", "--reason", "enroll build machines"}, &out, &bytes.Buffer{})
		if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "team administrator") {
			t.Fatalf("role=%q session issuance: %v", role, err)
		}
	}
	var count int
	if err := teststore.SQL(t, st).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused requests created sessions: %d %v", count, err)
	}
}

func TestAdminSessionRefusesLocalDeployment(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if err := openState(t).Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runAdminFixture([]string{"session", "--team", "default", "--reason", "headless enrollment"}, &out, &bytes.Buffer{})
	if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "licensed team or Cloud controller") {
		t.Fatalf("local session: %v", err)
	}
}

func TestAdminSessionDefaultsToPlainForEnrollmentPipe(t *testing.T) {
	st, _, _ := adminSessionFixture(t)
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','ops-admin','owner',1)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var out bytes.Buffer
	if err := runAdminFixture([]string{"session", "--team", "default", "--reason", "enroll build machines"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(out.String())))
	var count int
	if err := teststore.SQL(t, st).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sessions WHERE hash=?`, hex.EncodeToString(digest[:])).Scan(&count); err != nil || count != 1 {
		t.Fatal("default output cannot be used as an enrollment session")
	}
}
