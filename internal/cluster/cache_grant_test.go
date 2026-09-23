package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/cache"
)

const (
	operatorCacheToken = "operator-cache-token"
	cacheGrantKey      = "cache-grant-key"
)

// newGrantingController stands in for the controller's cache-grant route: it
// resolves the runner token to its team and signs with the grant key, which
// only the controller and the cache hold.
func newGrantingController(t *testing.T, teams map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/runs/{id}/cache-grant", func(w http.ResponseWriter, r *http.Request) {
		bearer, _ := bytes.CutPrefix([]byte(r.Header.Get("Authorization")), []byte("Bearer "))
		team, ok := teams[string(bearer)]
		if !ok {
			http.Error(w, "no team", http.StatusForbidden)
			return
		}
		grant, err := authwire.MintCacheGrant(cacheGrantKey, team, r.PathValue("id"), time.Now(), time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"grant": grant, "team": team})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newGrantCache(t *testing.T) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	cfg := cache.DefaultConfig()
	cfg.DataDir = root
	cfg.ProxyDir = filepath.Join(root, "proxy")
	cfg.SSHKeyDir = filepath.Join(root, "no-ssh-key")
	cfg.APIToken = operatorCacheToken
	cfg.GrantKey = cacheGrantKey
	s, err := cache.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// A run in team A gets a grant that names team A, and that grant cannot reach
// team B's bins: a binary team B planted under team A's pipeline key is never
// what team A's launcher runs. The launcher holds no operator cache token.
func TestTriggerRunGrantConfinesTheBinCacheToTheRunsTeam(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles a pipeline binary")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv(authwire.CacheTokenEnv, "")
	t.Setenv(authwire.CacheGrantEnv, "")
	ctx := context.Background()
	logger := discardLogger()
	ctrl := newGrantingController(t, map[string]string{"runner-a": "team-a", "runner-b": "team-b"})
	cacheSrv := newGrantCache(t)

	grantA := requestRunCacheGrant(ctx, ctrl.URL, "runner-a", "run-a", logger)
	claims, err := authwire.VerifyCacheGrant(cacheGrantKey, grantA, time.Now())
	if err != nil || claims.Team != "team-a" || claims.Run != "run-a" {
		t.Fatalf("team A's grant = %+v, %v; want team-a for run-a", claims, err)
	}
	grantB := requestRunCacheGrant(ctx, ctrl.URL, "runner-b", "run-b", logger)

	pipeline := writeTrivialPipeline(t)
	key, err := bincache.PipelineCacheKey(pipeline)
	if err != nil {
		t.Fatal(err)
	}
	poison := filepath.Join(t.TempDir(), "poison")
	if err := os.WriteFile(poison, []byte("#!/bin/sh\necho team-b owns you\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bincache.UploadBinary(ctx, cacheSrv.URL, grantB, key, poison); err != nil {
		t.Fatalf("team B uploading into its own tree: %v", err)
	}

	binary, err := triggerBuildOrFetchBinary(ctx, pipeline, TriggerLoopOptions{
		ControllerURL: ctrl.URL,
		GitcacheURL:   cacheSrv.URL,
		Token:         "runner-a",
	}, grantA, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.release()
	if binary.cache != binaryCacheCompiled {
		t.Fatalf("team A's binary came from %q; team B's planted bin reached team A", binary.cache)
	}
	built, err := os.ReadFile(binary.path)
	if err != nil {
		t.Fatal(err)
	}

	fetched := filepath.Join(t.TempDir(), "fetched")
	if err := bincache.TryBinary(ctx, cacheSrv.URL, grantA, key, fetched); err != nil {
		t.Fatalf("team A reading back its own upload: %v", err)
	}
	if got, _ := os.ReadFile(fetched); !bytes.Equal(got, built) {
		t.Fatal("team A's grant read something other than team A's own binary")
	}
	if err := bincache.TryBinary(ctx, cacheSrv.URL, grantB, key, fetched); err != nil {
		t.Fatalf("team B reading its own bin: %v", err)
	}
	if got, _ := os.ReadFile(fetched); bytes.Equal(got, built) {
		t.Fatal("team B's grant read team A's binary")
	}
	if err := bincache.TryBinary(ctx, cacheSrv.URL, operatorCacheToken, key, fetched); !errors.Is(err, bincache.ErrMiss) {
		t.Fatalf("the operator's unscoped tree = %v, want a miss: grants must not write there", err)
	}
}

// A controller that predates the route answers 404, and the run proceeds
// without the cache instead of failing.
func TestRequestRunCacheGrantDegradesOnAnOlderController(t *testing.T) {
	ctrl := httptest.NewServer(http.NotFoundHandler())
	defer ctrl.Close()
	if grant := requestRunCacheGrant(context.Background(), ctrl.URL, "runner-a", "run-a", discardLogger()); grant != "" {
		t.Fatalf("grant = %q from a controller with no grant route", grant)
	}
}
