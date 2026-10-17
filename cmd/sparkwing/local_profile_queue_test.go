package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestLocalQueueCacheDoesNotDependOnOwnerWinningJobClaim(t *testing.T) {
	for _, unit := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(unit), func(t *testing.T) {
			home := t.TempDir()
			owned, err := teststore.Open(filepath.Join(home, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owned.Close() })
			tenant := teststore.ForTeam(t, owned, store.DefaultTeam)
			if err := teststore.SeedRun(owned, t.Context(), store.DefaultTeam, store.Run{ID: "run", Pipeline: "ci", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			repo := initSnapshotRepo(t)
			runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			run.Request.Request.Origin = runspec.OriginLocal
			run.Request.Request.Source.Location.LocalCheckout = repo
			run.Request.Request.Options.CacheFrom.Value = "cloud"
			raw, err := json.Marshal(run.Request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owned.DB().ExecContext(t.Context(), `UPDATE runs SET request_json=? WHERE id='run'`, raw); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(home, "config.yaml")
			if err := os.WriteFile(config, []byte("profiles:\n  cloud:\n    controller:\n      url: https://profile.test\n      token: account-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SPARKWING_CONFIG", config)
			previous := http.DefaultTransport
			issued, revoked, closed := 0, 0, 0
			http.DefaultTransport = queueHostTransport(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				switch r.Method + " " + r.URL.Path {
				case "POST /api/v1/tokens":
					issued++
					w.WriteHeader(http.StatusCreated)
					_, _ = w.WriteString(`{"capability":"reader-token","prefix":"reader","team":"default","principal":"account","grants":["data.read"]}`)
				case "DELETE /api/v1/tokens/reader":
					revoked++
					w.WriteHeader(http.StatusNoContent)
				default:
					return nil, fmt.Errorf("unexpected profile request: %s %s", r.Method, r.URL)
				}
				return w.Result(), nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			principal := capability.Principal{Capability: capability.Capability{Team: "default", Unit: unit, Binding: capability.Binding{RunID: "run", NodeID: "job"}}}
			binding := runners.StoreBinding{Tenant: tenant, Principal: principal, Bearer: func(context.Context) (string, error) { return "award", nil }}
			services, err := localNativeServices(t.Context(), home, owned, binding, client.EngineOptions{URL: "http://local"}, func(context.Context, localcontroller.NativeConfig) (runners.NativeServices, error) {
				return runners.NativeServices{Close: func() error { closed++; return nil }}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			bound := services.Storage.RemoteCache != nil
			if err := services.Close(); err != nil {
				t.Fatal(err)
			}
			if !bound || issued != 1 || revoked != 1 || closed != 1 {
				t.Fatalf("cache depends on claim winner: bound=%v issued=%d revoked=%d closed=%d", bound, issued, revoked, closed)
			}
		})
	}
}
