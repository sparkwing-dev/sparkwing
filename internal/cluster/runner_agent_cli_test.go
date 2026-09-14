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

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

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

	cfg, err := agentconfig.Validate(agentconfig.Config{
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

func TestRunAgentCLI_RefusesTheRemovedEnrolledKeysBeforePolling(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"name", "controller: http://127.0.0.1:1\nname: desk\ntoken: tok\n"},
		{"coordinators", "coordinators:\n  - controller: http://127.0.0.1:1\n    token: tok\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := fssecure.SecurePrivateConfig(path); err != nil {
				t.Fatal(err)
			}
			err := runAgentCLI([]string{"--config", path})
			if err == nil || !strings.Contains(err.Error(), agentconfig.EnrolledModeRemoved) {
				t.Fatalf("runAgentCLI = %v, want %q", err, agentconfig.EnrolledModeRemoved)
			}
		})
	}
}
