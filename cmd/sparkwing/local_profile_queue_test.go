package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestLocalQueueCacheDoesNotDependOnOwnerWinningJobClaim(t *testing.T) {
	for _, unit := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(unit), func(t *testing.T) {
			home := t.TempDir()
			unit := controllerwire.ClaimedUnit{Unit: unit, RunID: "run"}
			run := &unit.Request
			repo := initSnapshotRepo(t)
			runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			run.Request.Origin = runspec.OriginLocal
			run.Request.Git.Repository.Key = "git:example.test/acme/repo"
			run.Request.Source.Location.LocalCheckout = repo
			run.Request.Options.CacheFrom.Value = "cloud"
			run.Request.EnvironmentSource = runspec.EnvironmentCaptured
			snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
			if err != nil {
				t.Fatal(err)
			}
			run.Request.Env, err = snapshots.Capture(t.Context(), []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home})
			if err != nil {
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
			services := nodehost.NativeServices{Close: func() error { closed++; return nil }}
			err = bindLocalProfiles(t.Context(), home, client.EngineOptions{URL: "http://local"}, unit, &services)
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
