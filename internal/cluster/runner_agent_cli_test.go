package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentConfig_RoundTripFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	yaml := `
controller: http://localhost:4344
logs: http://localhost:4345
gitcache: http://localhost:4344/api/v1/gitcache
cache_token: cache-abc
profile: dev
token: tok-abc
max_concurrent: 3
labels:
  - laptop
  - arch=arm64
  - "  "
spawn_policy: return-to-queue
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	if cfg.Controller != "http://localhost:4344" {
		t.Fatalf("controller: %q", cfg.Controller)
	}
	if cfg.Gitcache != "http://localhost:4344/api/v1/gitcache" || cfg.CacheToken != "cache-abc" {
		t.Fatalf("gitcache credentials: url=%q token=%q", cfg.Gitcache, cfg.CacheToken)
	}
	if cfg.Token != "tok-abc" || cfg.MaxConcurrent != 3 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
	norm, err := ValidateAgentConfig(*cfg)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(norm.Labels) != 2 || norm.Labels[0] != "laptop" || norm.Labels[1] != "arch=arm64" {
		t.Fatalf("labels normalization: %v", norm.Labels)
	}
	if norm.Poll <= 0 || norm.Lease <= 0 {
		t.Fatalf("defaults missing: %+v", norm)
	}
}

func TestAgentConfig_RejectsMissingController(t *testing.T) {
	_, err := ValidateAgentConfig(AgentConfig{Token: "x"})
	if err == nil {
		t.Fatal("expected error for missing controller")
	}
}

func TestAgentConfig_RejectsUnsupportedSpawnPolicy(t *testing.T) {
	for _, policy := range []string{"run-local", "auto", "bogus"} {
		_, err := ValidateAgentConfig(AgentConfig{
			Controller:  "http://x",
			SpawnPolicy: policy,
		})
		if err == nil {
			t.Fatalf("spawn_policy=%q should be rejected", policy)
		}
	}
}

func TestAgentConfig_DefaultsSpawnPolicy(t *testing.T) {
	norm, err := ValidateAgentConfig(AgentConfig{Controller: "http://x"})
	if err != nil {
		t.Fatal(err)
	}
	if norm.SpawnPolicy != "return-to-queue" {
		t.Fatalf("default: %q", norm.SpawnPolicy)
	}
	if norm.Gitcache != "http://x/api/v1/gitcache" {
		t.Fatalf("gitcache default = %q, want controller proxy", norm.Gitcache)
	}
}

func TestAgent_ClaimPassesLabelsAndToken(t *testing.T) {
	var seen atomic.Value
	claimSeen := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/nodes/claim", func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); !strings.HasPrefix(h, "Bearer ") {
			t.Errorf("missing bearer header: %q", h)
		}
		var body struct {
			HolderID string   `json:"holder_id"`
			Labels   []string `json:"labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen.Store(&seenClaim{
			auth:   r.Header.Get("Authorization"),
			labels: body.Labels,
			holder: body.HolderID,
		})
		w.WriteHeader(http.StatusNoContent)
		select {
		case claimSeen <- struct{}{}:
		default:
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg, err := ValidateAgentConfig(AgentConfig{
		Controller:    srv.URL,
		Token:         "bearer-xyz",
		Labels:        []string{"laptop", "arch=arm64"},
		MaxConcurrent: 1,
		Poll:          50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- RunPoolLoop(ctx, PoolLoopConfig{
			ControllerURL: cfg.Controller,
			Token:         cfg.Token,
			HolderPrefix:  "agent:test",
			Labels:        cfg.Labels,
			MaxConcurrent: cfg.MaxConcurrent,
			PollInterval:  cfg.Poll,
			Lease:         cfg.Lease,
			SourceName:    "agent",
		}, nil)
	}()
	select {
	case <-claimSeen:
		cancel()
	case <-ctx.Done():
		t.Fatal("agent never made a claim call")
	}
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("agent did not stop after claim observation")
	}
	if err := runErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("RunPoolLoop: %v", err)
	}

	got, _ := seen.Load().(*seenClaim)
	if got == nil {
		t.Fatal("agent never made a claim call")
	}
	if got.auth != "Bearer bearer-xyz" {
		t.Fatalf("auth header: %q", got.auth)
	}
	if len(got.labels) != 2 || got.labels[0] != "laptop" || got.labels[1] != "arch=arm64" {
		t.Fatalf("labels: %v", got.labels)
	}
	if !strings.HasPrefix(got.holder, "agent:test:") {
		t.Fatalf("holder prefix: %q", got.holder)
	}
	if elapsed := time.Since(started); elapsed >= 300*time.Millisecond {
		t.Fatalf("claim observation took %s, want less than 300ms", elapsed)
	}
}

type seenClaim struct {
	auth   string
	labels []string
	holder string
}
