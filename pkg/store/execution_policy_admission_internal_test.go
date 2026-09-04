package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
)

func TestAssistedPrepareRefusalOrderAndPrivateBinding(t *testing.T) {
	st, claimant := newAssistedAdmissionStore(t)
	defer st.Close()
	seedAssistedAdmissionNode(t, st, "candidate", nil)
	if err := st.HeartbeatExecutor(context.Background(), claimant, "helper",
		ExecutorResource{Cores: 4, MemoryBytes: 4 << 30}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}

	sink := executionpolicy.NewPreparationSink()
	ctx := executionpolicy.WithPreparationSink(context.Background(), sink)
	if _, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper"); !errors.Is(err, executionpolicy.ErrExecutionUpgradeRequired) {
		t.Fatalf("missing runtime report error = %v, want upgrade required", err)
	}
	var upgrade *executionpolicy.UpgradeRequiredError
	if _, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper"); !errors.As(err, &upgrade) ||
		upgrade.Scope != "supervisor" || !containsExact(upgrade.Missing, executionpolicy.FleetSupervisorRequirement) {
		t.Fatalf("upgrade detail = %#v, %v", upgrade, err)
	}

	report := executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "linux", GOARCH: "amd64",
	})
	tooNew := report
	tooNew.BodyProtocolMinimum = executionpolicy.AssistedBodyProtocolVersion + 1
	tooNew.BodyProtocolMaximum = tooNew.BodyProtocolMinimum
	heartbeatAssistedExecutor(t, st, claimant, tooNew)
	if _, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper"); !errors.Is(err, executionpolicy.ErrExecutionProtocolMismatch) {
		t.Fatalf("newer-only helper error = %v, want protocol incompatible", err)
	}

	heartbeatAssistedExecutor(t, st, claimant, report)
	if preparation, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper"); preparation == nil ||
		!errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("compatible un-attested prepare = (%+v, %v)", preparation, err)
	}
	binding := sink.Load()
	if binding.IsZero() || binding.RunID != "run" || binding.NodeID != "candidate" {
		t.Fatalf("private preparation binding = %+v", binding)
	}

	summary, err := st.SchedulingSummary(context.Background(), "run", "candidate")
	if err != nil {
		t.Fatal(err)
	}
	offer := ExecutorClaimOffer{
		ExecutorName: "helper", HolderID: "holder", RunID: "run", NodeID: "candidate",
		ReservationID: "reservation", ResourceDigest: summary.ResourceDigest, Slot: 0,
	}
	if _, err := st.OfferExecutorClaim(context.Background(), claimant, offer); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("offer without prepared binding = %v, want stale refusal", err)
	}
	offerCtx, err := executionpolicy.WithOfferBinding(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.OfferExecutorClaim(offerCtx, claimant, offer); !errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("bound offer error = %v, want body attestation", err)
	}
	var offers, claimed int
	if err := st.queryRow(context.Background(), `SELECT COUNT(*) FROM node_claim_offers`).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if err := st.queryRow(context.Background(), `SELECT COUNT(*) FROM nodes WHERE claimed_by IS NOT NULL`).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if offers != 0 || claimed != 0 {
		t.Fatalf("refused assisted path mutated state: offers=%d claimed=%d", offers, claimed)
	}
}

func TestAssistedPrepareSkipsUnsealedAndSixtyFourIneligiblePoliciesBeforeDecode(t *testing.T) {
	st, recorder := newRecordingExecutorStore(t)
	claimant := initializeAssistedAdmissionStore(t, st)
	report := executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "windows", GOARCH: "amd64",
	})
	heartbeatAssistedExecutor(t, st, claimant, report)
	if err := st.CreateNode(context.Background(), Node{RunID: "run", NodeID: "unsealed", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(context.Background(), "run", "unsealed"); err != nil {
		t.Fatal(err)
	}
	for i := range 65 {
		nodeID := fmt.Sprintf("ineligible-%03d", i)
		seedAssistedAdmissionNode(t, st, nodeID, []string{"missing-capability"})
		if _, err := st.exec(context.Background(), `UPDATE nodes SET execution_policy_json = ? WHERE run_id = ? AND node_id = ?`,
			[]byte(`{"corrupt":`), "run", nodeID); err != nil {
			t.Fatal(err)
		}
	}
	seedAssistedAdmissionNode(t, st, "eligible", nil)
	sink := executionpolicy.NewPreparationSink()
	ctx := executionpolicy.WithPreparationSink(context.Background(), sink)
	recorder.reset()
	preparation, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper")
	if preparation == nil || preparation.Summary.NodeID != "eligible" || !errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("scan beyond ineligible policies = (%+v, %v)", preparation, err)
	}
	if sink.Load().NodeID != "eligible" {
		t.Fatalf("binding selected %q, want eligible", sink.Load().NodeID)
	}
	statements := recorder.snapshot()
	if len(statements) != 9 {
		t.Fatalf("prepare statements = %d, want fixed snapshot count 9:\n%s", len(statements), strings.Join(statements, "\n---\n"))
	}
	if full := countFullPolicyReads(statements); full != 1 {
		t.Fatalf("full policy reads = %d, want only the selected candidate", full)
	}
}

func TestAssistedPrepareBoundedCursorDoesNotStarveCompatibleNode(t *testing.T) {
	st, claimant := newAssistedAdmissionStore(t)
	defer st.Close()
	report := executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "windows", GOARCH: "amd64",
	})
	heartbeatAssistedExecutor(t, st, claimant, report)
	for i := range executorPrepareCandidateLimit {
		seedAssistedAdmissionNode(t, st, fmt.Sprintf("ineligible-%03d", i), []string{"missing-capability"})
	}
	seedAssistedAdmissionNode(t, st, "eligible-after-bound", nil)
	ctx := executionpolicy.WithPreparationSink(context.Background(), executionpolicy.NewPreparationSink())
	if preparation, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper"); !errors.Is(err, ErrNotFound) || preparation != nil {
		t.Fatalf("bounded first prepare = (%+v, %v), want empty page", preparation, err)
	}
	preparation, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper")
	if preparation == nil || preparation.Summary.NodeID != "eligible-after-bound" ||
		!errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("continued prepare = (%+v, %v), want compatible candidate", preparation, err)
	}
}

func TestAssistedPrepareRuntimeRefusalDoesNotStarveLaterCompatiblePage(t *testing.T) {
	st, recorder := newRecordingExecutorStore(t)
	claimant := initializeAssistedAdmissionStore(t, st)
	report := executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "windows", GOARCH: "amd64",
	})
	heartbeatAssistedExecutor(t, st, claimant, report)
	seedAssistedAdmissionNodeWithPolicy(t, st, "runtime-incompatible", nil, func(policy *executionpolicy.NodeExecutionPolicy) {
		policy.SupervisorRequirements = append(policy.SupervisorRequirements, "future-supervisor-v9")
	})
	for i := 1; i < executorPrepareCandidateLimit; i++ {
		seedAssistedAdmissionNode(t, st, fmt.Sprintf("hard-ineligible-%03d", i), []string{"missing-capability"})
	}
	seedAssistedAdmissionNode(t, st, "compatible-after-bound", nil)

	ctx := executionpolicy.WithPreparationSink(context.Background(), executionpolicy.NewPreparationSink())
	recorder.reset()
	var upgrade *executionpolicy.UpgradeRequiredError
	if preparation, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper"); preparation != nil ||
		!errors.As(err, &upgrade) || !upgrade.SafeHold || upgrade.MinimumRelease != "" {
		t.Fatalf("bounded runtime refusal = (%+v, %#v, %v), want unresolved safe hold", preparation, upgrade, err)
	}
	if reads := countPrepareCandidateReads(recorder.snapshot()); reads != 1 {
		t.Fatalf("initial candidate reads = %d, want one bounded range", reads)
	}
	recorder.reset()
	preparation, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper")
	if preparation == nil || preparation.Summary.NodeID != "compatible-after-bound" ||
		!errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("continued runtime prepare = (%+v, %v), want compatible candidate", preparation, err)
	}
	if reads := countPrepareCandidateReads(recorder.snapshot()); reads != 2 {
		t.Fatalf("wrapped candidate reads = %d, want two bounded ranges", reads)
	}
}

func TestAssistedPrepareCandidateRangesUseIndexWithoutTempSort(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	now := time.Now()
	cursor := executorPrepareCursor{readyAt: now.UnixNano(), runID: "run", nodeID: "node"}
	for _, rangeKind := range []executorPrepareRange{
		executorPrepareFromStart,
		executorPrepareAfterCursor,
		executorPrepareThroughCursor,
	} {
		query, args := executorPrepareCandidateQuery("", "helper", now, rangeKind, cursor, executorPrepareCandidateLimit)
		rows, err := st.query(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			details = append(details, detail)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		_ = rows.Close()
		plan := strings.Join(details, "\n")
		if !strings.Contains(plan, "idx_nodes_assisted_claimable") {
			t.Fatalf("range %d query plan did not use assisted index:\n%s", rangeKind, plan)
		}
		if strings.Contains(strings.ToUpper(plan), "TEMP B-TREE") {
			t.Fatalf("range %d query plan sorts the qualifying queue:\n%s", rangeKind, plan)
		}
	}
}

func TestAssistedPrepareFiltersOversizedPolicyBeforeTransfer(t *testing.T) {
	st, claimant := newAssistedAdmissionStore(t)
	defer st.Close()
	report := executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "linux", GOARCH: "amd64",
	})
	heartbeatAssistedExecutor(t, st, claimant, report)
	seedAssistedAdmissionNode(t, st, "oversized", nil)
	if _, err := st.exec(context.Background(), `UPDATE nodes SET execution_policy_json = ? WHERE run_id = ? AND node_id = ?`,
		[]byte(strings.Repeat("x", executionpolicy.MaxEncodedPolicyBytes+1)), "run", "oversized"); err != nil {
		t.Fatal(err)
	}
	seedAssistedAdmissionNode(t, st, "bounded", nil)
	sink := executionpolicy.NewPreparationSink()
	preparation, err := st.PrepareNextExecutorClaim(executionpolicy.WithPreparationSink(context.Background(), sink), claimant, "helper")
	if preparation == nil || preparation.Summary.NodeID != "bounded" || !errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("oversized policy filter = (%+v, %v)", preparation, err)
	}
}

func TestAssistedPrepareBoundsRequirementMetadataAndFullPolicyReads(t *testing.T) {
	st, recorder := newRecordingExecutorStore(t)
	claimant := initializeAssistedAdmissionStore(t, st)
	heartbeatAssistedExecutor(t, st, claimant, executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "linux", GOARCH: "amd64",
	}))
	seedAssistedAdmissionNode(t, st, "oversized-requirements", nil)
	if _, err := st.exec(context.Background(), `UPDATE nodes SET execution_supervisor_requirements_json = ?
WHERE run_id = 'run' AND node_id = 'oversized-requirements'`,
		[]byte(strings.Repeat("x", executionpolicy.MaxRuntimeRequirementsJSONBytes+1))); err != nil {
		t.Fatal(err)
	}
	seedAssistedAdmissionNode(t, st, "corrupt-metadata", nil)
	if _, err := st.exec(context.Background(), `UPDATE nodes SET execution_supervisor_requirements_json = '["fleet-supervisor-v1"'
WHERE run_id = 'run' AND node_id = 'corrupt-metadata'`); err != nil {
		t.Fatal(err)
	}
	recorder.reset()
	if preparation, err := st.PrepareNextExecutorClaim(context.Background(), claimant, "helper"); !errors.Is(err, executionpolicy.ErrExecutionPolicyInvalid) || preparation != nil {
		t.Fatalf("corrupt metadata prepare = (%+v, %v)", preparation, err)
	}
	if full := countFullPolicyReads(recorder.snapshot()); full != 0 {
		t.Fatalf("corrupt metadata caused %d full policy reads, want zero", full)
	}

	if _, err := st.exec(context.Background(), `UPDATE nodes SET execution_supervisor_requirements_json = ?
WHERE run_id = 'run' AND node_id = 'corrupt-metadata'`, []byte(strings.Repeat("x", executionpolicy.MaxRuntimeRequirementsJSONBytes+1))); err != nil {
		t.Fatal(err)
	}
	seedAssistedAdmissionNode(t, st, "corrupt-policy", nil)
	if _, err := st.exec(context.Background(), `UPDATE nodes SET execution_policy_json = '{"version":'
WHERE run_id = 'run' AND node_id = 'corrupt-policy'`); err != nil {
		t.Fatal(err)
	}
	recorder.reset()
	ctx := executionpolicy.WithPreparationSink(context.Background(), executionpolicy.NewPreparationSink())
	if preparation, err := st.PrepareNextExecutorClaim(ctx, claimant, "helper"); !errors.Is(err, executionpolicy.ErrExecutionPolicyInvalid) || preparation != nil {
		t.Fatalf("corrupt policy prepare = (%+v, %v)", preparation, err)
	}
	if full := countFullPolicyReads(recorder.snapshot()); full != 1 {
		t.Fatalf("corrupt policy full reads = %d, want exactly one", full)
	}
}

func TestExecutorHeartbeatOwnsRuntimeReportAndOldHeartbeatClearsIt(t *testing.T) {
	st, claimant := newAssistedAdmissionStore(t)
	defer st.Close()
	report := executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v9.8.7", Commit: "observational-only",
		GOOS: "windows", GOARCH: "amd64",
	})
	heartbeatAssistedExecutor(t, st, claimant, report)
	got, err := st.executorRuntimeReport(context.Background(), "helper")
	if err != nil || !reflect.DeepEqual(got, report) {
		t.Fatalf("persisted runtime report = (%+v, %v), want %+v", got, err, report)
	}
	if err := st.HeartbeatExecutor(context.Background(), claimant, "helper",
		ExecutorResource{Cores: 4, MemoryBytes: 4 << 30}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	cleared, err := st.executorRuntimeReport(context.Background(), "helper")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.BodyProtocolMinimum != 0 || cleared.BodyProtocolMaximum != 0 || len(cleared.Supervisor) != 0 ||
		len(cleared.BodyHost) != 0 || cleared.Build != (buildinfo.Identity{}) {
		t.Fatalf("old heartbeat retained stale runtime authority: %+v", cleared)
	}
}

func newAssistedAdmissionStore(t *testing.T) (*Store, ClaimIdentity) {
	t.Helper()
	st := newTestStore(t)
	return st, initializeAssistedAdmissionStore(t, st)
}

func initializeAssistedAdmissionStore(t *testing.T, st *Store) ClaimIdentity {
	t.Helper()
	claimant := ClaimIdentity{Principal: "helper-principal", TokenPrefix: "swr_helper"}
	if err := st.EnrollExecutor(context.Background(), claimant.TokenPrefix, Executor{
		Name: "helper", Kind: "agent", Location: "local", Principal: claimant.Principal,
		BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 1,
		Budget: ExecutorResource{Cores: 4, MemoryBytes: 4 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(context.Background(), Run{
		ID: "run", Pipeline: "release", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return claimant
}

func heartbeatAssistedExecutor(t *testing.T, st *Store, claimant ClaimIdentity, report executionpolicy.RuntimeReport) {
	t.Helper()
	ctx, err := executionpolicy.WithRuntimeReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.HeartbeatExecutor(ctx, claimant, "helper", ExecutorResource{Cores: 4, MemoryBytes: 4 << 30}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func seedAssistedAdmissionNode(t *testing.T, st *Store, nodeID string, needs []string) {
	seedAssistedAdmissionNodeWithPolicy(t, st, nodeID, needs, nil)
}

func seedAssistedAdmissionNodeWithPolicy(t *testing.T, st *Store, nodeID string, needs []string,
	mutate func(*executionpolicy.NodeExecutionPolicy),
) {
	t.Helper()
	if err := st.CreateNode(context.Background(), Node{
		RunID: "run", NodeID: nodeID, Status: "pending", NeedsLabels: needs, RequestedSlots: 1,
	}); err != nil {
		t.Fatal(err)
	}
	policy := testExecutionPolicy()
	policy.NodeID = nodeID
	policy.Dependencies = nil
	policy.SecretNames = nil
	policy.AllowedLocations = []string{"cloud", "local"}
	if mutate != nil {
		mutate(&policy)
	}
	if err := st.markNodeReadyWithExecutionPolicy(context.Background(), "run", nodeID, policy); err != nil {
		t.Fatal(err)
	}
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countFullPolicyReads(statements []string) int {
	count := 0
	for _, statement := range statements {
		normalized := strings.Join(strings.Fields(strings.ToLower(statement)), " ")
		if strings.Contains(normalized, "from nodes where run_id = ? and node_id = ?") &&
			strings.Contains(normalized, "execution_policy_json") {
			count++
		}
	}
	return count
}

func countPrepareCandidateReads(statements []string) int {
	count := 0
	for _, statement := range statements {
		normalized := strings.Join(strings.Fields(strings.ToLower(statement)), " ")
		if strings.Contains(normalized, "from nodes n") &&
			strings.Contains(normalized, "execution_supervisor_requirements_json") {
			count++
		}
	}
	return count
}
