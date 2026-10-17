package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestRunnersPersonalEnrollmentCLIThroughController(t *testing.T) {
	for _, failService := range []bool{false, true} {
		t.Run(map[bool]string{false: "register", true: "rollback"}[failService], func(t *testing.T) {
			f := newRunnersFixture(t)
			state, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := state.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := state.AsOperator().CreateTeam(t.Context(), "personal-test"); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{
				`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`,
				`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('personal-test','member','editor',1)`,
			} {
				if _, err := state.DB().ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			pub, priv := licensetest.NewKey(t)
			now := time.Now()
			terms := licensetest.Terms{Features: []string{license.FeatureMultiTeam}, IssuedTo: "test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
			server := controller.New(state, nil).WithSecretsCipher(cipher).WithLicense(license.Resolve(licensetest.Sign(t, priv, terms), pub, now, nil))
			session, _, _, err := state.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "personal-test", time.Hour)
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
			args := []string{"--profile", "prod", "--runner", "desk", "--personal", "--session-stdin", "--repos", "github.com/acme/*", "--config", f.config, "--output", "json"}
			if failService {
				f.failExec["systemctl --user daemon-reload"] = "installation refused"
			} else {
				args = append(args, "--no-service")
			}
			captureStdout(t, func() { err = runRunnersAdd(args) })
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
