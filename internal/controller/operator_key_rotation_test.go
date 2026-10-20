package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type keyRotationFixture struct {
	st                 *store.Store
	old, current, next *secrets.Cipher
	stray              *secrets.Cipher
	teams              []store.Team
	operator, owner    string
}

func keyRotationCipher(t *testing.T, fill byte, previous ...byte) *secrets.Cipher {
	t.Helper()
	key := bytes.Repeat([]byte{fill}, secrets.KeySize)
	var c *secrets.Cipher
	var err error
	if len(previous) == 0 {
		c, err = secrets.NewCipher(key)
	} else {
		c, err = secrets.NewCipherWithPrevious(key, bytes.Repeat([]byte{previous[0]}, secrets.KeySize))
	}
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func seal(t *testing.T, c *secrets.Cipher, purpose secrets.Purpose, team store.Team, name, scope, plain string) string {
	t.Helper()
	v, err := c.SealBound(purpose, string(team), name, scope, false, true, plain)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func newKeyRotationFixture(t *testing.T, open func(*testing.T, ...store.Option) *store.Store) *keyRotationFixture {
	t.Helper()
	ctx := context.Background()
	f := &keyRotationFixture{old: keyRotationCipher(t, 1), current: keyRotationCipher(t, 2, 1), next: keyRotationCipher(t, 2), stray: keyRotationCipher(t, 9)}
	st := open(t, store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		return controller.New(st, nil).WithSecretsCipher(f.old).StorePolicies(), nil
	}))
	f.st = st
	raw, _, err := teststore.ProvisionCredential(ctx, st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap})
	if err != nil {
		t.Fatal(err)
	}
	f.operator = "Bearer " + raw
	if _, err := st.AsOperator().IdentityLinkStateKey(ctx); err != nil {
		t.Fatal(err)
	}
	alice, teamA := signUp(t, st, "alice")
	_, teamB := signUp(t, st, "bob")
	f.teams = []store.Team{teamA, teamB}
	f.owner = session(t, st, alice, teamA)
	now := time.Now()
	for _, team := range f.teams {
		if err := teststore.SeedSecret(st, team, store.Secret{Name: "TOKEN", Pipeline: "build", Masked: true, Principal: "seed", Value: seal(t, f.old, secrets.PurposeSecret, team, "TOKEN", "build", "secret-"+string(team))}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := teststore.SQL(t, st).ExecContext(ctx, `INSERT INTO git_credentials(id,team,host,kind,secret,created_at,updated_at) VALUES ($1,$2,'git.example','https',$3,1,1)`,
			"gc-"+string(team), team, seal(t, f.old, secrets.PurposeGitCredential, team, "git-credential:git.example", "git-credential", "git-"+string(team))); err != nil {
			t.Fatal(err)
		}
		seedRun(t, st, forTeam(t, st, team), "run-"+string(team), "build")
		if _, err := teststore.SQL(t, st).ExecContext(ctx, `UPDATE runs SET submitted_env=$1 WHERE team=$2 AND id=$3`, seal(t, f.old, secrets.PurposeRunEnvironment, team, "run-"+string(team), "run-environment", "env-"+string(team)), team, "run-"+string(team)); err != nil {
			t.Fatal(err)
		}
	}
	if err := teststore.SeedSecret(st, teamB, store.Secret{Name: "LOST", Masked: true, Principal: "seed", Value: seal(t, f.stray, secrets.PurposeSecret, teamB, "LOST", "", "lost")}, now); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *keyRotationFixture) server(t *testing.T, c *secrets.Cipher) http.Handler {
	t.Helper()
	return controller.NewDeployment(f.st, deploymenttest.Cloud(t, "https://controller.test"), nil).WithSecretsCipher(c).Handler()
}

func (f *keyRotationFixture) rotate(t *testing.T, h http.Handler, auth string, query ...string) (int, map[string]any) {
	t.Helper()
	target := "https://controller.test/api/v1/operator/secrets/rotate"
	if len(query) > 0 {
		target += "?" + query[0]
	}
	r := httptest.NewRequest(http.MethodPost, target, nil)
	r.Header.Set("Authorization", auth)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func (f *keyRotationFixture) opensUnder(t *testing.T, c *secrets.Cipher) (secretsOpen, git, env int, csrf bool) {
	t.Helper()
	ctx := context.Background()
	for _, team := range f.teams {
		var secret, cred, run string
		if err := teststore.SQL(t, f.st).QueryRowContext(ctx, `SELECT value FROM secrets WHERE team=$1 AND name='TOKEN'`, team).Scan(&secret); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SQL(t, f.st).QueryRowContext(ctx, `SELECT secret FROM git_credentials WHERE team=$1`, team).Scan(&cred); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SQL(t, f.st).QueryRowContext(ctx, `SELECT submitted_env FROM runs WHERE team=$1 AND id=$2`, team, "run-"+string(team)).Scan(&run); err != nil {
			t.Fatal(err)
		}
		if v, err := c.OpenBound(secrets.PurposeSecret, string(team), "TOKEN", "build", false, true, secret); err == nil && v == "secret-"+string(team) {
			secretsOpen++
		}
		if v, err := c.OpenBound(secrets.PurposeGitCredential, string(team), "git-credential:git.example", "git-credential", false, true, cred); err == nil && v == "git-"+string(team) {
			git++
		}
		if v, err := c.OpenBound(secrets.PurposeRunEnvironment, string(team), "run-"+string(team), "run-environment", false, true, run); err == nil && v == "env-"+string(team) {
			env++
		}
	}
	var envelope []byte
	if err := teststore.SQL(t, f.st).QueryRowContext(ctx, `SELECT value FROM settings WHERE key='session_csrf_key'`).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	_, err := c.OpenBound(secrets.PurposeSetting, "", "session_csrf_key", "settings", false, true, string(envelope))
	return secretsOpen, git, env, err == nil
}

func TestOperatorKeyRotationResealsEveryTenantUnderTheCurrentKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*testing.T, ...store.Option) *store.Store
	}{{"sqlite", openSQLiteStore}, {"postgres", openPostgresStore}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newKeyRotationFixture(t, tc.open)
			h := f.server(t, f.current)
			if code, body := f.rotate(t, h, f.owner); code != http.StatusForbidden && code != http.StatusUnauthorized {
				t.Fatalf("team owner rotated the deployment key: %d %v", code, body)
			}
			if s, g, e, c := f.opensUnder(t, f.next); s+g+e != 0 || c {
				t.Fatal("refused rotation resealed rows")
			}
			code, body := f.rotate(t, h, f.operator, "verify=1")
			if code != http.StatusOK || body["previous_key_only"] != float64(7) || body["secrets"] != float64(2) {
				t.Fatalf("verification: %d %v", code, body)
			}
			if s, g, e, c := f.opensUnder(t, f.next); s+g+e != 0 || c {
				t.Fatal("verification resealed rows")
			}
			if code, _ := f.rotate(t, h, f.operator, "verify=yes"); code != http.StatusBadRequest {
				t.Fatalf("malformed verify query: %d", code)
			}
			code, body = f.rotate(t, h, f.operator)
			if code != http.StatusOK || body["previous_key_only"] != float64(7) || body["secrets"] != float64(2) || body["git_credentials"] != float64(2) || body["run_environments"] != float64(2) || body["settings"] != float64(1) {
				t.Fatalf("rotation: %d %v", code, body)
			}
			unreadable, _ := body["unreadable"].([]any)
			if len(unreadable) != 1 || unreadable[0].(map[string]any)["name"] != "LOST" || unreadable[0].(map[string]any)["team"] != string(f.teams[1]) {
				t.Fatalf("unreadable rows: %v", body["unreadable"])
			}
			if s, g, e, c := f.opensUnder(t, f.next); s != 2 || g != 2 || e != 2 || !c {
				t.Fatalf("after rotation the current key alone opens %d secrets, %d git credentials, %d environments, session key %v", s, g, e, c)
			}
			var lost string
			if err := teststore.SQL(t, f.st).QueryRow(`SELECT value FROM secrets WHERE name='LOST'`).Scan(&lost); err != nil {
				t.Fatal(err)
			}
			if _, err := f.stray.OpenBound(secrets.PurposeSecret, string(f.teams[1]), "LOST", "", false, true, lost); err != nil {
				t.Fatalf("unreadable row changed: %v", err)
			}
			if code, body := f.rotate(t, h, f.operator, "verify=1"); code != http.StatusOK || body["previous_key_only"] != float64(0) {
				t.Fatalf("verification after rotation: %d %v", code, body)
			}
			if code, body := f.rotate(t, f.server(t, f.next), f.operator); code != http.StatusOK || body["secrets"] != float64(2) || body["previous_key_only"] != float64(0) {
				t.Fatalf("repeat rotation under the new key alone: %d %v", code, body)
			}
		})
	}
}
