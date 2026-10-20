package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnersPersonalEnrollmentCLIThroughController(t *testing.T) {
	for _, mode := range []string{"register", "rollback", "disabled", "shared", "settings"} {
		t.Run(mode, func(t *testing.T) {
			failService := mode == "rollback"
			f := newRunnersFixture(t)
			cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			var server *controller.Server
			state, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), store.WithPolicies(func(state *store.Store) (store.Policies, error) {
				server = controller.NewDeployment(state, deploymenttest.Team(t), nil).WithSecretsCipher(cipher)
				return server.StorePolicies(), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := state.Close(); err != nil {
					t.Error(err)
				}
			})
			teststore.SeedTeam(t, state, "personal-test")
			for _, query := range []string{
				`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`,
				`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('personal-test','member','editor',1)`,
			} {
				if _, err := teststore.SQL(t, state).ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "disabled" {
				if _, err := teststore.SQL(t, state).ExecContext(t.Context(), `UPDATE teams SET members_can_add_machines=0 WHERE name='personal-test'`); err != nil {
					t.Fatal(err)
				}
			}
			session, _, _, err := state.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "personal-test", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			withStdin(t, session+"\n")
			handler := server.Handler()
			original := http.DefaultTransport
			http.DefaultTransport = profileTransport(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w.Result(), nil
			})
			t.Cleanup(func() { http.DefaultTransport = original })
			if mode == "settings" {
				if _, err := teststore.SQL(t, state).ExecContext(t.Context(), `UPDATE memberships SET role='owner'`); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256([]byte(session))
				authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
				issued, err := state.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &authority, Team: "personal-test", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Grants: []capability.Grant{capability.RunsRead, capability.TeamAdmin}, Binding: capability.Binding{Kind: capability.BindingNone}})
				if err != nil {
					t.Fatal(err)
				}
				writeProfilesFixture(t, "profiles:\n  prod:\n    controller: { url: https://controller.example, token: "+issued.Raw+" }\n")
				for _, args := range [][]string{{"show"}, {"set", "--members-can-add-machines", "false"}, {"show"}} {
					var callErr error
					out := captureStdout(t, func() {
						callErr = runSparkwing(append([]string{"team", "settings"}, append(args, "--profile", "prod", "--output", "json")...))
					})
					if callErr != nil || !strings.Contains(out, `"members_can_add_machines":`) {
						t.Fatalf("settings CLI %v: %s %v", args, out, callErr)
					}
				}
				if _, err := teststore.SQL(t, state).ExecContext(t.Context(), `UPDATE memberships SET role='editor'`); err != nil {
					t.Fatal(err)
				}
				if err := runSparkwing([]string{"team", "settings", "set", "--profile", "prod", "--members-can-add-machines", "true"}); err == nil {
					t.Fatal("demoted admin changed team setting through CLI")
				}
				tenant := teststore.ForTeam(t, state, "personal-test")
				events, err := tenant.BusinessEvents(t.Context(), "team.settings_changed")
				if err != nil || len(events) != 1 || events[0].Actor != "member" {
					t.Fatalf("CLI setting audit: %+v %v", events, err)
				}
				return
			}
			args := []string{"--profile", "prod", "--runner", "desk", "--personal", "--session-stdin", "--repos", "github.com/acme/*", "--config", f.config, "--output", "json"}
			if mode == "shared" {
				args = slices.DeleteFunc(args, func(arg string) bool { return arg == "--personal" })
			}
			if failService {
				f.failExec["systemctl --user daemon-reload"] = "installation refused"
			} else {
				args = append(args, "--no-service")
			}
			captureStdout(t, func() { err = runRunnersAddArgs(args) })
			if mode == "disabled" || mode == "shared" {
				want := "members_can_add_machines"
				if mode == "shared" {
					want = "team administration"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal must name %s: %v", want, err)
				}
				var count int
				if err := teststore.SQL(t, state).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE subject_kind='machine'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("refused enrollment persisted credentials: %d %v", count, err)
				}
				return
			}
			if (err != nil) != failService {
				t.Fatalf("enrollment with failed service %v: %v", failService, err)
			}
			manager, err := client.NewSessionRunnerTokenManager(t.Context(), client.EngineOptions{URL: "https://controller.example"}, session)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := manager.ListTokens(t.Context(), controllerwire.TokenQuery{Kind: "runner", IncludeRevoked: true})
			if err != nil || len(inventory.Tokens) != 1 || (inventory.Tokens[0].RevokedAt != nil) != failService {
				t.Fatalf("enrollment rollback authority: %+v %v", inventory, err)
			}
			cfg, err := userconfig.ReadConfig(f.config)
			if err != nil || (cfg.Runner == nil) != failService {
				t.Fatalf("enrollment configuration: %+v %v", cfg, err)
			}
			if failService {
				if dirs, err := filepath.Glob(filepath.Join(filepath.Dir(f.config), "runner-credentials-*")); err != nil || len(dirs) != 0 {
					t.Fatalf("rollback credentials: %v %v", dirs, err)
				}
				return
			}
			boundary := errors.New("registered runner reached production composition")
			startup := cluster.RunnerCLI{Factory: func(ctx context.Context, options cluster.RunnerOptions) (runners.RuntimeConfig, error) {
				if options.Config.HolderPrefix == nil || *options.Config.HolderPrefix != "desk" {
					t.Error("enrollment lost the registered display name")
				}
				if raw, err := options.Bearer(ctx); err != nil || raw == "" {
					t.Errorf("enrolled credential was not available to startup: %v", err)
				}
				return runners.RuntimeConfig{}, boundary
			}}
			if err := startup.Run(t.Context(), "start", []string{"--config", f.config}, "development"); !errors.Is(err, boundary) {
				t.Fatalf("personal enrollment cannot start its registered runner: %v", err)
			}
			if _, err := manager.RevokeToken(t.Context(), inventory.Tokens[0].Prefix); err != nil {
				t.Fatal(err)
			}
		})
	}
}
