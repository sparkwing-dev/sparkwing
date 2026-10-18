package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestProfileCacheBranchIgnoresAmbientRepositoryOverrides(t *testing.T) {
	repo := initSnapshotRepo(t)
	runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	foreign := t.TempDir()
	property := func(raw []byte) bool {
		t.Setenv("GIT_DIR", filepath.Join(foreign, hex.EncodeToString(raw)+".git"))
		t.Setenv("GIT_WORK_TREE", foreign)
		got, err := checkoutDefaultBranch(t.Context(), repo)
		return err == nil && got == "main"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestProfileCacheConstructionAfterLocalIntake(t *testing.T) {
	options := productionLocalPolicyClient(t)
	engine, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "source", "source\n", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "source")
	runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	request := controllerwire.CreateRunRequest{IdempotencyKey: "profile-cache", Request: runspec.Request{
		Version: 1, Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Origin: runspec.OriginLocal,
		Source: runspec.Source{Ref: strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD")), Location: runspec.SourceLocation{LocalCheckout: repo, PipelineDir: ".sparkwing"}},
		Git:    runspec.Git{Repository: runspec.Repository{Key: "git:example.test/acme/repo"}, SHA: strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD")), DefaultBranch: runspec.Optional[string]{Set: true, Value: "main"}},
	}}
	admitted, err := engine.CreateRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Request.Request.Git.DefaultBranch.Set {
		t.Fatal("intake promoted requester branch metadata to trusted facts")
	}
	config := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(config, []byte("profiles:\n  cloud:\n    controller:\n      url: https://profile.test\n      token: account-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	previous := http.DefaultTransport
	issued, revoked := 0, 0
	http.DefaultTransport = queueHostTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if r.Header.Get("Authorization") != "Bearer account-token" {
			return nil, fmt.Errorf("profile construction used a different account")
		}
		response := httptest.NewRecorder()
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/tokens":
			var body struct {
				Grants []capability.Grant   `json:"grants"`
				Data   capability.DataScope `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			if len(body.Grants) != 1 || body.Grants[0] != capability.DataRead || body.Data.Use != capability.DataRemoteCache || body.Data.RepositoryKey != string(request.Request.Git.Repository.Key) || !body.Data.VerifiedDefaultOnly || len(body.Data.ReadRefs) != 1 || body.Data.ReadRefs[0] != "main" {
				return nil, fmt.Errorf("cache scope changed: %+v", body)
			}
			issued++
			response.WriteHeader(http.StatusCreated)
			_, _ = response.WriteString(`{"capability":"reader-token","prefix":"reader","team":"default","principal":"account","grants":["data.read"]}`)
		case "DELETE /api/v1/tokens/reader":
			revoked++
			response.WriteHeader(http.StatusNoContent)
		default:
			return nil, fmt.Errorf("unexpected profile request: %s %s", r.Method, r.URL)
		}
		return response.Result(), nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cache, closeCache, err := openProfileCache(ctx, "cloud", admitted.Request.Request.Git, admitted.Request.Request.Source.Location.LocalCheckout)
	if err != nil {
		t.Fatal("queued host could not construct its sealed profile cache:", err)
	}
	if cache == nil {
		t.Fatal("profile cache was not bound")
	}
	cancel()
	if err := closeCache(); err != nil {
		t.Fatal(err)
	}
	if issued != 1 || revoked != 1 {
		t.Fatalf("profile cache credential lifetime: issued=%d revoked=%d", issued, revoked)
	}
}
