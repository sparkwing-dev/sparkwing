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

// TestAgentConfig_RoundTripFromYAML exercises the file loader and
// validator: yaml -> struct -> normalized struct with defaults.
// Ensures label trimming, spawn_policy default, and required-field
// checks all fire.
func TestAgentConfig_RoundTripFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	yaml := `
controller: http://localhost:4344
logs: http://localhost:4345
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
	if cfg.Token != "tok-abc" || cfg.MaxConcurrent != 3 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
	norm, err := ValidateAgentConfig(*cfg)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	// Blank label gets stripped.
	if len(norm.Labels) != 2 || norm.Labels[0] != "laptop" || norm.Labels[1] != "arch=arm64" {
		t.Fatalf("labels normalization: %v", norm.Labels)
	}
	// Defaults applied for unspecified fields.
	if norm.Poll <= 0 || norm.Lease <= 0 {
		t.Fatalf("defaults missing: %+v", norm)
	}
}

// TestAgentConfig_RejectsMissingController fails loudly rather than
// letting an agent claim with a blank controller URL.
func TestAgentConfig_RejectsMissingController(t *testing.T) {
	_, err := ValidateAgentConfig(AgentConfig{Token: "x"})
	if err == nil {
		t.Fatal("expected error for missing controller")
	}
}

// TestAgentConfig_RejectsUnsupportedSpawnPolicy makes sure the two
// deferred policies (run-local / auto) fail validation so a human
// misconfiguration doesn't silently fall through to return-to-queue.
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

// TestAgentConfig_DefaultsSpawnPolicy fills in return-to-queue when
// unset so agents without an explicit policy don't silently break
// once run-local / auto ship.
func TestAgentConfig_DefaultsSpawnPolicy(t *testing.T) {
	norm, err := ValidateAgentConfig(AgentConfig{Controller: "http://x"})
	if err != nil {
		t.Fatal(err)
	}
	if norm.SpawnPolicy != "return-to-queue" {
		t.Fatalf("default: %q", norm.SpawnPolicy)
	}
}

// TestAgent_ClaimPassesLabelsAndToken is the wire-level test: stand
// up a mock controller, write an agent.yaml pointing at it, run the
// agent loop with a cancelable ctx, and assert the claim call
// carried both the bearer token and the configured labels.
func TestAgent_ClaimPassesLabelsAndToken(t *testing.T) {
	var seen atomic.Value // *seenClaim
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
		w.WriteHeader(http.StatusNoContent) // queue empty -> loop sleeps
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

	// Run the loop directly so we don't depend on signal.NotifyContext.
	if err := RunPoolLoop(ctx, PoolLoopConfig{
		ControllerURL: cfg.Controller,
		Token:         cfg.Token,
		HolderPrefix:  "agent:test",
		Labels:        cfg.Labels,
		MaxConcurrent: cfg.MaxConcurrent,
		PollInterval:  cfg.Poll,
		Lease:         cfg.Lease,
		SourceName:    "agent",
	}, nil); err != nil && !errors.Is(err, context.DeadlineExceeded) {
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
}

type seenClaim struct {
	auth   string
	labels []string
	holder string
}
