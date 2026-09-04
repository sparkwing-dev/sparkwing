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

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	if err := fssecure.SecurePrivateConfig(path); err != nil {
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

func TestAgentConfig_RejectsUnknownFieldsAndAdditionalDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"unknown field", "controller: http://localhost:4344\nadmin: true\n", "field admin not found"},
		{"second document", "controller: http://localhost:4344\n---\ntoken: hidden\n", "multiple YAML documents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := fssecure.SecurePrivateConfig(path); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadAgentConfig(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadAgentConfig error = %v, want %q", err, tc.want)
			}
		})
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

func TestAgentMembership_HeartbeatsAndFailsClosedWithoutWingd(t *testing.T) {
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
	go func() { done <- runAgentMembershipLoop(ctx, cfg, member, provider, ctrl, nil, nil, discardSlog()) }()
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
		func(context.Context) capacityReport { return capacityReport{} }, ctrl, nil, nil, discardSlog())
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

type offerSlotClient struct {
	preparation *store.ExecutorClaimPreparation
	prepare     func(context.Context) (*store.ExecutorClaimPreparation, error)
	offers      []client.ExecutorClaim
	results     []client.ExecutorClaimOfferResult
	offerErrors map[int]error
	offer       func(context.Context, client.ExecutorClaim, int) (client.ExecutorClaimOfferResult, error)
}

func (*offerSlotClient) HeartbeatExecutor(context.Context, string, client.Headroom) error { return nil }

func (c *offerSlotClient) PrepareExecutorClaim(ctx context.Context, _ string) (*store.ExecutorClaimPreparation, error) {
	if c.prepare != nil {
		return c.prepare(ctx)
	}
	if c.preparation != nil {
		if err := executionpolicy.StorePreparation(ctx, testExecutorClaimBinding()); err != nil {
			return nil, err
		}
	}
	return c.preparation, nil
}

func (c *offerSlotClient) OfferExecutorClaim(ctx context.Context, claim client.ExecutorClaim, _, _ string) (client.ExecutorClaimOfferResult, error) {
	if binding, ok := executionpolicy.OfferBindingFromContext(ctx); !ok || binding != testExecutorClaimBinding() {
		return client.ExecutorClaimOfferResult{}, errors.New("offer omitted prepared execution binding")
	}
	c.offers = append(c.offers, claim)
	if c.offer != nil {
		return c.offer(ctx, claim, len(c.offers))
	}
	if err := c.offerErrors[len(c.offers)]; err != nil {
		return client.ExecutorClaimOfferResult{}, err
	}
	result := c.results[0]
	c.results = c.results[1:]
	return result, nil
}

func testExecutorClaimBinding() executionpolicy.ClaimBinding {
	return executionpolicy.ClaimBinding{
		RunID: "run", NodeID: "node", PolicyHash: "sha256:policy", PolicyVersion: 1, BodyProtocol: 1,
		SupervisorRequirementsHash: "sha256:supervisor", BodyRequirementsHash: "sha256:body",
	}
}

func TestExecutorOfferSlotDoesNotReserveAfterBodyAttestationRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctrl := &offerSlotClient{prepare: func(context.Context) (*store.ExecutorClaimPreparation, error) {
		cancel()
		return nil, &executionpolicy.BodyAttestationRequiredError{RunID: "run", NodeID: "node"}
	}}
	var reservations atomic.Int64
	ledger := offerSlotLedger{reserve: func(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, executorCapacityLimits, int) (ExecutorCapacityReservation, error) {
		reservations.Add(1)
		return nil, errors.New("unexpected reservation")
	}}
	runExecutorOfferSlot(ctx, AgentConfig{Poll: time.Millisecond}, AgentCoordinatorConfig{Name: "desk"}, 123, 0,
		ctrl, ledger, nil, discardSlog())
	if reservations.Load() != 0 || len(ctrl.offers) != 0 {
		t.Fatalf("body-attestation refusal reserved=%d offered=%d", reservations.Load(), len(ctrl.offers))
	}
}

func TestExecutorOfferSlotDoesNotReserveOrRunAnUnsealedLegacyCandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctrl := &offerSlotClient{prepare: func(context.Context) (*store.ExecutorClaimPreparation, error) {
		cancel()
		return nil, nil
	}}
	var reservations, executions atomic.Int64
	ledger := offerSlotLedger{reserve: func(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, executorCapacityLimits, int) (ExecutorCapacityReservation, error) {
		reservations.Add(1)
		return nil, errors.New("unexpected reservation")
	}}
	exec := func(context.Context, *store.Node, string, *orchestrator.LocalAdmission) {
		executions.Add(1)
	}
	runExecutorOfferSlot(ctx, AgentConfig{Poll: time.Millisecond}, AgentCoordinatorConfig{Name: "desk"}, 123, 0,
		ctrl, ledger, exec, discardSlog())
	if reservations.Load() != 0 || len(ctrl.offers) != 0 || executions.Load() != 0 {
		t.Fatalf("unsealed legacy candidate reserved=%d offered=%d executed=%d",
			reservations.Load(), len(ctrl.offers), executions.Load())
	}
}

type offerSlotLedger struct {
	reserve func(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, executorCapacityLimits, int) (ExecutorCapacityReservation, error)
}

func (l offerSlotLedger) Reserve(ctx context.Context, summary store.ExecutorSchedulingSummary, membership store.ExecutorMembershipSnapshot, limits executorCapacityLimits, slot int) (ExecutorCapacityReservation, error) {
	return l.reserve(ctx, summary, membership, limits, slot)
}

type offerSlotReservation struct {
	id, membership, worker, runID, nodeID, digest string
	slot                                          int
	consumed, released                            atomic.Int64
}

func (r *offerSlotReservation) ID() string             { return r.id }
func (r *offerSlotReservation) MembershipID() string   { return r.membership }
func (r *offerSlotReservation) WorkerID() string       { return r.worker }
func (r *offerSlotReservation) RunID() string          { return r.runID }
func (r *offerSlotReservation) NodeID() string         { return r.nodeID }
func (r *offerSlotReservation) ResourceDigest() string { return r.digest }
func (r *offerSlotReservation) Slot() int              { return r.slot }
func (r *offerSlotReservation) ExecutionContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

func (r *offerSlotReservation) Consume() (*orchestrator.LocalAdmission, error) {
	r.consumed.Add(1)
	return &orchestrator.LocalAdmission{}, nil
}

func (r *offerSlotReservation) Release() error {
	r.released.Add(1)
	return nil
}

func TestExecutorOfferSlotPinsAcrossLostResponseAndConsumesThePreparedReservation(t *testing.T) {
	preparation := &store.ExecutorClaimPreparation{
		Summary:    store.ExecutorSchedulingSummary{RunID: "run", NodeID: "node", ResourceDigest: "sha256:digest", Slots: 1},
		Membership: store.ExecutorMembershipSnapshot{MembershipID: "membership", WorkerID: "desk", Eligible: true, MaxConcurrent: 1},
	}
	ctrl := &offerSlotClient{preparation: preparation, offerErrors: map[int]error{2: errors.New("response lost")}, results: []client.ExecutorClaimOfferResult{
		{Pending: true}, {Node: &store.Node{RunID: "run", NodeID: "node"}},
	}}
	reservation := &offerSlotReservation{id: "reservation", membership: "membership", worker: "desk", runID: "run", nodeID: "node", digest: "sha256:digest", slot: 0}
	ctx, cancel := context.WithCancel(context.Background())
	ledger := offerSlotLedger{reserve: func(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, executorCapacityLimits, int) (ExecutorCapacityReservation, error) {
		return reservation, nil
	}}
	executed := atomic.Int64{}
	exec := func(context.Context, *store.Node, string, *orchestrator.LocalAdmission) {
		executed.Add(1)
		cancel()
	}
	runExecutorOfferSlot(ctx, AgentConfig{Poll: time.Minute, Lease: time.Minute},
		AgentCoordinatorConfig{Name: "desk"}, 123, 0, ctrl, ledger, exec, discardSlog())
	if len(ctrl.offers) != 3 {
		t.Fatalf("offers = %d, want 3", len(ctrl.offers))
	}
	for _, offer := range ctrl.offers {
		if offer.ExecutorName != "desk" || offer.ReservationID != "reservation" || offer.ResourceDigest != "sha256:digest" || offer.Slot != 0 {
			t.Fatalf("offer = %+v", offer)
		}
	}
	if executed.Load() != 1 || reservation.consumed.Load() != 1 || reservation.released.Load() != 1 {
		t.Fatalf("execution=%d consume=%d release=%d", executed.Load(), reservation.consumed.Load(), reservation.released.Load())
	}
}

func TestExecutorOfferSlotWithholdsOfferWithoutImmediateCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctrl := &offerSlotClient{preparation: &store.ExecutorClaimPreparation{
		Summary:    store.ExecutorSchedulingSummary{RunID: "run", NodeID: "node", ResourceDigest: "sha256:digest", Slots: 1},
		Membership: store.ExecutorMembershipSnapshot{MembershipID: "membership", WorkerID: "desk", Eligible: true, MaxConcurrent: 1},
	}}
	ledger := offerSlotLedger{reserve: func(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, executorCapacityLimits, int) (ExecutorCapacityReservation, error) {
		cancel()
		return nil, ErrExecutorCapacityUnavailable
	}}
	runExecutorOfferSlot(ctx, AgentConfig{Poll: time.Millisecond}, AgentCoordinatorConfig{Name: "desk"}, 123, 0,
		ctrl, ledger, nil, discardSlog())
	if len(ctrl.offers) != 0 {
		t.Fatalf("offers without capacity = %d", len(ctrl.offers))
	}
}

func TestExecutorOfferSlotHonorsEnrollmentConcurrencyCeiling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	ctrl := &offerSlotClient{preparation: &store.ExecutorClaimPreparation{
		Summary:    store.ExecutorSchedulingSummary{RunID: "run", NodeID: "node", ResourceDigest: "sha256:digest", Slots: 1},
		Membership: store.ExecutorMembershipSnapshot{MembershipID: "membership", WorkerID: "desk", Eligible: true, MaxConcurrent: 1},
	}}
	var reservations atomic.Int64
	ledger := offerSlotLedger{reserve: func(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, executorCapacityLimits, int) (ExecutorCapacityReservation, error) {
		reservations.Add(1)
		return nil, errors.New("unexpected reservation")
	}}
	runExecutorOfferSlot(ctx, AgentConfig{Poll: time.Millisecond}, AgentCoordinatorConfig{Name: "desk"}, 123, 1,
		ctrl, ledger, nil, discardSlog())
	if reservations.Load() != 0 || len(ctrl.offers) != 0 {
		t.Fatalf("reservations = %d, offers = %d", reservations.Load(), len(ctrl.offers))
	}
}

func TestExecutorOfferSlotReleasesReservationWhenControllerHangsAfterFirstOffer(t *testing.T) {
	deadline := time.Now().Add(100 * time.Millisecond)
	preparation := &store.ExecutorClaimPreparation{
		Summary:       store.ExecutorSchedulingSummary{RunID: "run", NodeID: "node", ResourceDigest: "sha256:digest", Slots: 1},
		Membership:    store.ExecutorMembershipSnapshot{MembershipID: "membership", WorkerID: "desk", Eligible: true, MaxConcurrent: 1},
		OfferDeadline: &deadline,
	}
	ctrl := &offerSlotClient{preparation: preparation}
	ctrl.offer = func(ctx context.Context, _ client.ExecutorClaim, attempt int) (client.ExecutorClaimOfferResult, error) {
		if attempt == 1 {
			return client.ExecutorClaimOfferResult{Pending: true}, nil
		}
		<-ctx.Done()
		return client.ExecutorClaimOfferResult{}, ctx.Err()
	}
	reservation := &offerSlotReservation{id: "reservation", membership: "membership", worker: "desk", runID: "run", nodeID: "node", digest: "sha256:digest", slot: 0}
	ctx, cancel := context.WithCancel(context.Background())
	ledger := offerSlotLedger{reserve: func(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, executorCapacityLimits, int) (ExecutorCapacityReservation, error) {
		return reservation, nil
	}}
	started := time.Now()
	done := make(chan struct{})
	go func() {
		runExecutorOfferSlot(ctx, AgentConfig{Poll: time.Hour}, AgentCoordinatorConfig{Name: "desk"}, 123, 0,
			ctrl, ledger, nil, discardSlog())
		close(done)
	}()
	for reservation.released.Load() == 0 && time.Since(started) < time.Second {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if reservation.released.Load() != 1 {
		t.Fatalf("reservation releases = %d, want 1", reservation.released.Load())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hung offer held reservation for %s", elapsed)
	}
	if len(ctrl.offers) < 2 {
		t.Fatalf("offers = %d, want first response plus hung retry", len(ctrl.offers))
	}
}
