package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func enrollTestExecutor(t *testing.T, s *store.Store, name string, maxConcurrent int, cores float64) store.ClaimIdentity {
	t.Helper()
	identity := store.ClaimIdentity{Principal: "runner-fleet", TokenPrefix: "swr_" + name}
	err := s.EnrollExecutor(context.Background(), identity.TokenPrefix, store.Executor{
		Name: name, Kind: "agent", Location: "cloud", Principal: identity.Principal,
		Capabilities: []string{"linux", "gpu"}, BasePriority: 10, PriorityCeiling: 20,
		MaxConcurrent: maxConcurrent,
		Budget:        store.ExecutorResource{Cores: cores, MemoryBytes: 8 << 30},
	})
	if err != nil {
		t.Fatalf("EnrollExecutor: %v", err)
	}
	if err := s.HeartbeatExecutor(context.Background(), identity, name,
		store.ExecutorResource{Cores: cores, MemoryBytes: 8 << 30}, 0, time.Now()); err != nil {
		t.Fatalf("HeartbeatExecutor: %v", err)
	}
	return identity
}

func seedExecutorNode(t *testing.T, s *store.Store, runID string, cores float64, labels ...string) {
	t.Helper()
	ctx := context.Background()
	plan := []byte(`{"nodes":[{"id":"work","modifiers":{"res_cores":` + strconv.FormatFloat(cores, 'f', -1, 64) + `,"prefers":["fast"]}}]}`)
	if err := s.CreateRun(ctx, store.Run{ID: runID, Pipeline: "executor-test", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: "work", Status: "pending", NeedsLabels: labels}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := s.MarkNodeReady(ctx, runID, "work"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
}

func TestExecutorEnrollmentOwnsTrustAndHeartbeatOnlyNarrowsLiveness(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollTestExecutor(t, s, "worker-a", 2, 4)

	otherToken := store.ClaimIdentity{Principal: identity.Principal, TokenPrefix: "swr_rotated"}
	if err := s.HeartbeatExecutor(ctx, otherToken, "worker-a", store.ExecutorResource{Cores: 99}, 0, time.Now()); !errors.Is(err, store.ErrExecutorCredentialMismatch) {
		t.Fatalf("same-principal different-token heartbeat = %v", err)
	}
	for _, resource := range []store.ExecutorResource{{Cores: -1}, {Cores: math.NaN()}, {MemoryBytes: -1}} {
		if err := s.HeartbeatExecutor(ctx, identity, "worker-a", resource, 0, time.Now()); err == nil {
			t.Fatalf("invalid heartbeat resource %+v accepted", resource)
		}
	}
	if err := s.HeartbeatExecutor(ctx, identity, "worker-a", store.ExecutorResource{Cores: 3, MemoryBytes: 6 << 30}, 2, time.Now()); err != nil {
		t.Fatalf("valid heartbeat: %v", err)
	}

	listed, err := s.ListExecutors(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListExecutors = %+v, %v", listed, err)
	}
	got := listed[0]
	if got.Kind != "agent" || got.Location != "cloud" || got.BasePriority != 10 || got.PriorityCeiling != 20 || got.MaxConcurrent != 2 || got.Budget.Cores != 4 {
		t.Fatalf("heartbeat changed trusted envelope: %+v", got)
	}
	if !got.HeadroomReported || got.Headroom.Cores != 3 || got.QueueDepth != 2 {
		t.Fatalf("heartbeat did not update liveness: %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), identity.TokenPrefix) || strings.Contains(string(raw), "token_prefix") {
		t.Fatalf("executor JSON exposed token prefix: %s", raw)
	}

	if err := s.EnrollExecutor(ctx, identity.TokenPrefix, store.Executor{
		Name: "worker-a", Kind: "gateway", Location: "local", Principal: identity.Principal,
		Capabilities: []string{"linux"}, BasePriority: 30, PriorityCeiling: 40,
		MaxConcurrent: 1, Budget: store.ExecutorResource{Cores: 2},
	}); err != nil {
		t.Fatalf("admin update: %v", err)
	}
	updated, err := s.ExecutorForCredential(ctx, identity, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Kind != "gateway" || updated.BasePriority != 30 || updated.Headroom.Cores != 3 || updated.QueueDepth != 2 {
		t.Fatalf("admin update lost liveness or trust fields: %+v", updated)
	}
	if err := s.EnrollExecutor(ctx, otherToken.TokenPrefix, store.Executor{
		Name: "worker-a", Kind: "gateway", Location: "local", Principal: identity.Principal,
		Capabilities: []string{"linux"}, BasePriority: 30, PriorityCeiling: 40,
		MaxConcurrent: 1, Budget: store.ExecutorResource{Cores: 2},
	}); err != nil {
		t.Fatalf("credential rotation: %v", err)
	}
	rotated, err := s.ExecutorForCredential(ctx, otherToken, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.LastSeen.IsZero() || rotated.HeadroomReported || rotated.Headroom != (store.ExecutorResource{}) || rotated.QueueDepth != 0 {
		t.Fatalf("credential rotation retained old credential liveness: %+v", rotated)
	}
	if err := s.EnrollExecutor(ctx, otherToken.TokenPrefix, store.Executor{Name: "worker-b", Kind: "agent", Location: "unknown", MaxConcurrent: 1}); err == nil {
		t.Fatal("one exact credential was enrolled to two names")
	}
}

func TestProvisionExecutorRollbackRevokesCredentialAndRemovesExactEnrollment(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	now := time.Now().UTC()
	raw, tok, err := s.ProvisionExecutor(ctx, "desk-owner", store.Executor{
		Name: "desk", Kind: "agent", Location: "local", BasePriority: 50,
		PriorityCeiling: 100, MaxConcurrent: 1,
	}, []string{"nodes.claim"}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackExecutorProvisioning(ctx, "desk", tok.Prefix, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupToken(raw, now.Add(time.Second)); !errors.Is(err, store.ErrTokenRevoked) {
		t.Fatalf("rolled-back token authentication = %v", err)
	}
	if _, err := s.ExecutorForCredential(ctx, store.ClaimIdentity{Principal: "desk-owner", TokenPrefix: tok.Prefix}, "desk"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rolled-back enrollment lookup = %v", err)
	}
}

func TestResetExecutorLivenessPreventsStaleTrustFromRemainingEligible(t *testing.T) {
	s := newStoreT(t)
	identity := enrollTestExecutor(t, s, "removed-worker", 1, 2)
	if err := s.ResetExecutorLiveness(context.Background()); err != nil {
		t.Fatal(err)
	}
	executor, err := s.ExecutorForCredential(context.Background(), identity, "removed-worker")
	if err != nil {
		t.Fatal(err)
	}
	if !executor.LastSeen.IsZero() || executor.HeadroomReported || executor.Headroom != (store.ExecutorResource{}) || executor.QueueDepth != 0 {
		t.Fatalf("stale liveness survived reset: %+v", executor)
	}
}

func TestExecutorSchedulingSummaryAndMembershipAreReadOnlyAndLocationNeutral(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollTestExecutor(t, s, "worker-a", 2, 4)
	seedExecutorNode(t, s, "run-summary", 2, "linux")

	summary, err := s.SchedulingSummary(ctx, "run-summary", "work")
	if err != nil {
		t.Fatalf("SchedulingSummary: %v", err)
	}
	if summary.ResourceDigest == "" || summary.Slots != 1 || summary.Resources.Cores != 2 || len(summary.PreferredCapabilities) != 1 || summary.PreferredCapabilities[0] != "fast" {
		t.Fatalf("summary = %+v", summary)
	}
	summary.RunPriority = 50
	membership, err := s.ResolveExecutorMembership(ctx, identity, "worker-a", summary)
	if err != nil {
		t.Fatalf("ResolveExecutorMembership: %v", err)
	}
	if !membership.Eligible || membership.EffectivePriority != 20 || membership.HighestEligibleCeiling != 20 || membership.MembershipID == "" {
		t.Fatalf("membership = %+v", membership)
	}
	summary.HardCapabilities = []string{"cloud"}
	membership, err = s.ResolveExecutorMembership(ctx, identity, "worker-a", summary)
	if err != nil {
		t.Fatalf("ResolveExecutorMembership location check: %v", err)
	}
	if membership.Eligible {
		t.Fatal("display location was treated as a trusted capability")
	}
}

func TestExecutorClaimBindsExactPreparedNodeCredentialSlotAndDigest(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollTestExecutor(t, s, "worker-a", 2, 4)
	seedExecutorNode(t, s, "older-other", 1, "linux")
	seedExecutorNode(t, s, "prepared", 1.5, "linux")
	summary, err := s.SchedulingSummary(ctx, "prepared", "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, identity, "worker-a", "prepared", "work", "executor:worker-a:claim", time.Minute, "reservation-a", 0, "wrong-digest"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong digest = %v, want ErrLockHeld", err)
	}
	if _, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, identity, "worker-a", "prepared", "work", "", time.Minute, "reservation-a", 0, summary.ResourceDigest); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("missing holder = %v, want ErrLockHeld", err)
	}
	other := store.ClaimIdentity{Principal: identity.Principal, TokenPrefix: "swr_other"}
	if _, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, other, "worker-a", "prepared", "work", "executor:worker-a:claim", time.Minute, "reservation-a", 0, summary.ResourceDigest); !errors.Is(err, store.ErrExecutorCredentialMismatch) {
		t.Fatalf("wrong credential = %v", err)
	}
	wrongPrincipal := store.ClaimIdentity{Principal: "other-principal", TokenPrefix: identity.TokenPrefix}
	if _, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, wrongPrincipal, "worker-a", "prepared", "work", "executor:worker-a:claim", time.Minute, "reservation-a", 0, summary.ResourceDigest); !errors.Is(err, store.ErrExecutorCredentialMismatch) {
		t.Fatalf("wrong principal audit identity = %v", err)
	}
	n, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, identity, "worker-a", "prepared", "work", "executor:worker-a:claim", time.Minute, "reservation-a", 0, summary.ResourceDigest)
	if err != nil || n == nil || n.RunID != "prepared" {
		t.Fatalf("exact claim = %+v, %v", n, err)
	}
	older, err := s.GetNode(ctx, "older-other", "work")
	if err != nil || older.ClaimedBy != "" {
		t.Fatalf("unoffered older node was claimed: %+v, %v", older, err)
	}
	if err := s.ValidateExecutorClaimReservation(ctx, identity, n.RunID, n.NodeID, "worker-a", "reservation-a", 0, summary.ResourceDigest); err != nil {
		t.Fatalf("validate exact binding: %v", err)
	}
	if err := s.ValidateExecutorClaimReservation(ctx, identity, n.RunID, n.NodeID, "worker-a", "wrong", 0, summary.ResourceDigest); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong reservation = %v, want ErrLockHeld", err)
	}
	otherIdentity := enrollTestExecutor(t, s, "worker-b", 1, 2)
	seedExecutorNode(t, s, "other-executor", 1, "linux")
	otherSummary, err := s.SchedulingSummary(ctx, "other-executor", "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, otherIdentity, "worker-b", "other-executor", "work", "executor:worker-b:claim", time.Minute, "reservation-a", 0, otherSummary.ResourceDigest); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reservation reused across executors = %v, want ErrLockHeld", err)
	}
}

func TestExecutorClaimPersistsSummaryChargeAndRejectsProfileDrift(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollTestExecutor(t, s, "worker-a", 2, 2.5)
	seedExecutorNode(t, s, "run-charge", 1.5, "linux")
	summary, err := s.SchedulingSummary(ctx, "run-charge", "work")
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte(`{"nodes":[{"id":"work","modifiers":{"res_cores":2}}]}`)
	if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET plan_json = ? WHERE id = ?`, changed, "run-charge"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, identity, "worker-a", "run-charge", "work", "executor:worker-a:claim", time.Minute, "reservation-a", 0, summary.ResourceDigest); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("stale summary digest = %v, want ErrLockHeld", err)
	}
	summary, err = s.SchedulingSummary(ctx, "run-charge", "work")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, identity, "worker-a", "run-charge", "work", "executor:worker-a:claim", time.Minute, "reservation-a", 0, summary.ResourceDigest)
	if err != nil {
		t.Fatalf("claim after reprepare: %v", err)
	}
	var cores float64
	var memory int64
	if err := s.DB().QueryRowContext(ctx, `SELECT claim_cores, claim_memory_bytes FROM nodes WHERE run_id = ? AND node_id = ?`, n.RunID, n.NodeID).Scan(&cores, &memory); err != nil {
		t.Fatal(err)
	}
	if cores != summary.Resources.Cores || memory != summary.Resources.MemoryBytes {
		t.Fatalf("persisted charge = {%v %d}, summary = %+v", cores, memory, summary.Resources)
	}
}
