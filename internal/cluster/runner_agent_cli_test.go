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

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
	if norm.LocalAdmission == nil || *norm.LocalAdmission {
		t.Fatal("legacy singular config did not preserve disabled local admission")
	}
}

func TestAgentConfig_RequiresLocalAdmissionOnlyForEnrolledMode(t *testing.T) {
	disabled := false
	legacy, err := ValidateAgentConfig(AgentConfig{Controller: "http://x", LocalAdmission: &disabled})
	if err != nil || legacy.LocalAdmission == nil || *legacy.LocalAdmission {
		t.Fatalf("legacy local_admission:false = %+v, %v", legacy.LocalAdmission, err)
	}
	if _, err := ValidateAgentConfig(AgentConfig{Name: "desk", Controller: "http://x", Token: "swr_x", LocalAdmission: &disabled}); err == nil {
		t.Fatal("enrolled local_admission:false was accepted")
	}
	enrolled, err := ValidateAgentConfig(AgentConfig{Name: "desk", Controller: "http://x", Token: "swr_x"})
	if err != nil || enrolled.LocalAdmission == nil || !*enrolled.LocalAdmission {
		t.Fatalf("enrolled local admission default = %+v, %v", enrolled.LocalAdmission, err)
	}
}

func TestAgentConfig_MultipleMembershipsRequireDistinctCredentialsAndShareCeilings(t *testing.T) {
	cfg := AgentConfig{
		Name: "desk", MaxConcurrent: 3, Contribution: "4,8gb",
		Coordinators: []AgentCoordinatorConfig{
			{Controller: "https://personal.example", Token: "swr_personal", MaxConcurrent: 2},
			{Controller: "https://team.example", Token: "swr_team", MaxConcurrent: 9, Contribution: "2,4gb"},
		},
	}
	norm, err := ValidateAgentConfig(cfg)
	if err != nil {
		t.Fatalf("ValidateAgentConfig: %v", err)
	}
	if norm.Coordinators[0].Name != "desk" || norm.Coordinators[1].Name != "desk" ||
		norm.Coordinators[0].Contribution != "4,8gb" || norm.Coordinators[1].MaxConcurrent != 3 {
		t.Fatalf("membership ceilings = %+v", norm.Coordinators)
	}
	cfg.Coordinators[1].Token = "swr_personal"
	if _, err := ValidateAgentConfig(cfg); err == nil {
		t.Fatal("duplicate membership credential was accepted")
	}
}

func TestAgentMembership_HeartbeatsOnlyAndFailsClosedWithoutWingd(t *testing.T) {
	var heartbeats atomic.Int64
	var claims atomic.Int64
	heartbeatSeen := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agents/desk/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		heartbeats.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer swr_member" {
			t.Errorf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
		select {
		case heartbeatSeen <- struct{}{}:
		default:
		}
	})
	mux.HandleFunc("POST /api/v1/nodes/claim", func(w http.ResponseWriter, _ *http.Request) {
		claims.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := AgentConfig{Heartbeat: time.Millisecond}
	member := AgentCoordinatorConfig{Name: "desk", Controller: srv.URL, Token: "swr_member"}
	ctrl := client.NewWithToken(srv.URL, srv.Client(), member.Token)
	provider := func(context.Context) capacityReport {
		return capacityReport{headroom: &client.Headroom{Cores: 2, MemoryBytes: 4 << 30}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentMembershipLoop(ctx, cfg, member, provider, ctrl, discardSlog()) }()
	select {
	case <-heartbeatSeen:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("executor heartbeat was not sent")
	}
	if err := <-done; err != nil {
		t.Fatalf("membership loop: %v", err)
	}
	before := heartbeats.Load()
	err := runAgentMembershipLoop(context.Background(), cfg, member,
		func(context.Context) capacityReport { return capacityReport{} }, ctrl, discardSlog())
	if err == nil || !strings.Contains(err.Error(), "heartbeat withheld") {
		t.Fatalf("unavailable wingd error = %v", err)
	}
	if got := heartbeats.Load(); got != before {
		t.Fatalf("heartbeats after failed probe = %d, want %d", got, before)
	}
	if got := claims.Load(); got != 0 {
		t.Fatalf("legacy claims = %d, want 0", got)
	}
}

func TestAgentMembershipSupervisors_IsolateTerminalFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	healthyStarted := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- superviseAgentMembership(ctx, func(context.Context) error {
			return errors.New("coordinator unavailable")
		}, discardSlog())
	}()
	go func() {
		done <- superviseAgentMembership(ctx, func(runCtx context.Context) error {
			close(healthyStarted)
			<-runCtx.Done()
			return runCtx.Err()
		}, discardSlog())
	}()
	select {
	case <-healthyStarted:
	case <-time.After(time.Second):
		t.Fatal("healthy membership was cancelled by peer failure")
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("supervisor exit = %v", err)
		}
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
