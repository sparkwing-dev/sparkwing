package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrExecutorCredentialMismatch means an executor is enrolled to another
// exact token prefix.
var ErrExecutorCredentialMismatch = errors.New("executor credential does not match enrollment")

// ExecutorRegistrationActiveWindow is how long the last successful heartbeat
// remains eligible before the executor is reported offline.
const ExecutorRegistrationActiveWindow = 2 * time.Minute

// ExecutorResource is a CPU and memory capacity or charge.
type ExecutorResource struct {
	Cores       float64 `json:"cores" yaml:"cores"`
	MemoryBytes int64   `json:"memory_bytes" yaml:"memory_bytes"`
}

// Executor is one administrator-enrolled execution membership.
type Executor struct {
	Name             string           `json:"name"`
	TokenPrefix      string           `json:"-"`
	Kind             string           `json:"kind"`
	Location         string           `json:"location"`
	Capabilities     []string         `json:"capabilities,omitempty"`
	BasePriority     int              `json:"base_priority"`
	PriorityCeiling  int              `json:"priority_ceiling"`
	MaxConcurrent    int              `json:"max_concurrent"`
	Budget           ExecutorResource `json:"budget"`
	Principal        string           `json:"principal,omitempty"`
	LastSeen         time.Time        `json:"last_seen"`
	HeadroomReported bool             `json:"-"`
	Headroom         ExecutorResource `json:"headroom"`
	QueueDepth       int              `json:"queue_depth"`
}

// EnrollExecutor creates or updates an administrator-owned executor envelope.
// tokenPrefix is the exact non-secret credential prefix allowed to use it.
func (s *Store) EnrollExecutor(ctx context.Context, tokenPrefix string, e Executor) error {
	if tokenPrefix == "" {
		return errors.New("executor token prefix is required")
	}
	if e.BasePriority < 0 || e.BasePriority > 100 || e.PriorityCeiling < e.BasePriority || e.PriorityCeiling > 100 {
		return errors.New("executor priority must satisfy 0 <= base_priority <= priority_ceiling <= 100")
	}
	if e.MaxConcurrent < 1 || e.Budget.Cores < 0 || math.IsNaN(e.Budget.Cores) || math.IsInf(e.Budget.Cores, 0) || e.Budget.MemoryBytes < 0 {
		return errors.New("executor limits must be finite and non-negative with max_concurrent >= 1")
	}
	caps, err := json.Marshal(e.Capabilities)
	if err != nil {
		return err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
UPDATE executors
   SET last_seen = CASE WHEN token_prefix = ? THEN last_seen ELSE 0 END,
       headroom_reported = CASE WHEN token_prefix = ? THEN headroom_reported ELSE 0 END,
       headroom_cores = CASE WHEN token_prefix = ? THEN headroom_cores ELSE 0 END,
       headroom_memory_bytes = CASE WHEN token_prefix = ? THEN headroom_memory_bytes ELSE 0 END,
       queue_depth = CASE WHEN token_prefix = ? THEN queue_depth ELSE 0 END,
       token_prefix = ?, kind = ?, location = ?, capabilities_json = ?,
       base_priority = ?, priority_ceiling = ?, max_concurrent = ?,
       budget_cores = ?, budget_memory_bytes = ?, principal = ?
 WHERE name = ?`,
		tokenPrefix, tokenPrefix, tokenPrefix, tokenPrefix, tokenPrefix,
		tokenPrefix, e.Kind, e.Location, caps, e.BasePriority, e.PriorityCeiling, e.MaxConcurrent,
		e.Budget.Cores, e.Budget.MemoryBytes, e.Principal, e.Name)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		_, err = tx.ExecContext(ctx, `
INSERT INTO executors
    (name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
     budget_cores, budget_memory_bytes, principal, last_seen,
     headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 0)`,
			e.Name, tokenPrefix, e.Kind, e.Location, caps, e.BasePriority, e.PriorityCeiling, e.MaxConcurrent,
			e.Budget.Cores, e.Budget.MemoryBytes, e.Principal)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HeartbeatExecutor refreshes only liveness and measured headroom after the
// exact enrolled credential proves possession.
func (s *Store) HeartbeatExecutor(ctx context.Context, claimant ClaimIdentity, name string, headroom ExecutorResource, queueDepth int, now time.Time) error {
	if claimant.TokenPrefix == "" || headroom.Cores < 0 || math.IsNaN(headroom.Cores) || math.IsInf(headroom.Cores, 0) || headroom.MemoryBytes < 0 || queueDepth < 0 {
		return errors.New("executor heartbeat requires a credential and finite non-negative headroom")
	}
	result, err := s.exec(ctx, `
UPDATE executors
   SET last_seen = ?, headroom_reported = 1, headroom_cores = ?, headroom_memory_bytes = ?, queue_depth = ?
 WHERE name = ? AND token_prefix = ?`,
		now.UnixNano(), headroom.Cores, headroom.MemoryBytes, queueDepth, name, claimant.TokenPrefix)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return fmt.Errorf("%w: %s", ErrExecutorCredentialMismatch, name)
	}
	return nil
}

// ExecutorForCredential returns an enrollment only when name and the exact
// authenticated token prefix match.
func (s *Store) ExecutorForCredential(ctx context.Context, claimant ClaimIdentity, name string) (Executor, error) {
	e, err := s.getExecutor(ctx, name)
	if err != nil {
		return Executor{}, err
	}
	if claimant.TokenPrefix == "" || e.TokenPrefix != claimant.TokenPrefix {
		return Executor{}, fmt.Errorf("%w: %s", ErrExecutorCredentialMismatch, name)
	}
	return e, nil
}

// ExecutorNameForTokenPrefix reports the enrollment bound to tokenPrefix.
func (s *Store) ExecutorNameForTokenPrefix(ctx context.Context, tokenPrefix string) (string, error) {
	var name string
	err := s.queryRow(ctx, `SELECT name FROM executors WHERE token_prefix = ?`, tokenPrefix).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// ListExecutors returns every registered executor, including stale entries.
func (s *Store) ListExecutors(ctx context.Context) ([]Executor, error) {
	rows, err := s.query(ctx, `
SELECT name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
       budget_cores, budget_memory_bytes, principal, last_seen,
       headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth
  FROM executors ORDER BY kind, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Executor
	for rows.Next() {
		var e Executor
		var caps []byte
		var seen int64
		var reported int
		if err := rows.Scan(&e.Name, &e.TokenPrefix, &e.Kind, &e.Location, &caps, &e.BasePriority, &e.PriorityCeiling, &e.MaxConcurrent,
			&e.Budget.Cores, &e.Budget.MemoryBytes, &e.Principal, &seen,
			&reported, &e.Headroom.Cores, &e.Headroom.MemoryBytes, &e.QueueDepth); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(caps, &e.Capabilities)
		if seen > 0 {
			e.LastSeen = time.Unix(0, seen)
		}
		e.HeadroomReported = reported != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExecutorActivity reports live claims without collapsing multiple slots from
// the same run into one job.
type ExecutorActivity struct {
	RunIDs      []string
	ActiveSlots int
}

// ActiveExecutorActivity returns live run IDs and claim counts by registered
// executor.
func (s *Store) ActiveExecutorActivity(ctx context.Context, now time.Time) (map[string]ExecutorActivity, error) {
	rows, err := s.query(ctx, `
SELECT claim_executor, run_id, COUNT(*) FROM nodes
 WHERE claim_executor != '' AND claimed_by IS NOT NULL
   AND lease_expires_at >= ? AND `+nodeNotDone+`
 GROUP BY claim_executor, run_id ORDER BY claim_executor, run_id`, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ExecutorActivity{}
	for rows.Next() {
		var name, runID string
		var slots int
		if err := rows.Scan(&name, &runID, &slots); err != nil {
			return nil, err
		}
		activity := out[name]
		activity.RunIDs = append(activity.RunIDs, runID)
		activity.ActiveSlots += slots
		out[name] = activity
	}
	return out, rows.Err()
}

// ExecutorSchedulingSummary is the read-only input helper admission and
// controller arbitration share for one node. Preferred capabilities order
// equally eligible executors; they never weaken HardCapabilities.
type ExecutorSchedulingSummary struct {
	RunID                 string           `json:"run_id"`
	NodeID                string           `json:"node_id"`
	HardCapabilities      []string         `json:"hard_capabilities,omitempty"`
	PreferredCapabilities []string         `json:"preferred_capabilities,omitempty"`
	Resources             ExecutorResource `json:"resources"`
	ResourceDigest        string           `json:"resource_digest"`
	Slots                 int              `json:"slots"`
	RunPriority           int              `json:"run_priority"`
	RequiredCoordinatorID string           `json:"required_coordinator_id,omitempty"`
	RequiredLocation      string           `json:"required_location,omitempty"`
}

// ExecutorMembershipSnapshot binds a registered executor to the exact
// controller membership credential without exposing that credential.
type ExecutorMembershipSnapshot struct {
	MembershipID           string `json:"membership_id"`
	WorkerID               string `json:"worker_id"`
	Kind                   string `json:"kind"`
	RegisteredBasePriority int    `json:"registered_base_priority"`
	Eligible               bool   `json:"eligible"`
	EffectivePriority      int    `json:"effective_priority"`
	HighestEligibleCeiling int    `json:"highest_eligible_ceiling"`
	ActiveSlots            int    `json:"active_slots"`
	MaxConcurrent          int    `json:"max_concurrent"`
}

// SchedulingSummary resolves the resource charge and hard/preferred labels
// for a persisted node without changing claim or ready-queue state.
func (s *Store) SchedulingSummary(ctx context.Context, runID, nodeID string) (ExecutorSchedulingSummary, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return ExecutorSchedulingSummary{}, err
	}
	defer func() { _ = tx.Rollback() }()
	n := &Node{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID), n); err != nil {
		return ExecutorSchedulingSummary{}, err
	}
	charge, err := s.executorNodeCharge(ctx, tx, n)
	if err != nil {
		return ExecutorSchedulingSummary{}, err
	}
	var plan []byte
	if err := tx.QueryRowContext(ctx, `SELECT plan_json FROM runs WHERE id = ?`, runID).Scan(&plan); err != nil {
		return ExecutorSchedulingSummary{}, err
	}
	return ExecutorSchedulingSummary{
		RunID: runID, NodeID: nodeID, HardCapabilities: append([]string(nil), n.NeedsLabels...),
		PreferredCapabilities: snapshotNodePrefers(plan, nodeID), Resources: charge, ResourceDigest: executorResourceDigest(charge), Slots: 1,
		RunPriority: snapshotRunPriority(plan), RequiredCoordinatorID: n.RequiredCoordinatorID,
		RequiredLocation: n.RequiredExecutorLocation,
	}, nil
}

// ResolveExecutorMembership applies hard capability/resource/slot filters
// before computing priority. Run priority and preferred-capability order may
// adjust ordering only within the operator's configured ceiling.
func (s *Store) ResolveExecutorMembership(ctx context.Context, claimant ClaimIdentity, executorName string, summary ExecutorSchedulingSummary) (ExecutorMembershipSnapshot, error) {
	e, err := s.getExecutor(ctx, executorName)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	if e.TokenPrefix != claimant.TokenPrefix || claimant.TokenPrefix == "" {
		return ExecutorMembershipSnapshot{}, fmt.Errorf("%w: %s", ErrExecutorCredentialMismatch, executorName)
	}
	active, eligible, err := s.executorEligible(ctx, e, summary)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	eligible = eligible && !e.LastSeen.Before(time.Now().Add(-ExecutorRegistrationActiveWindow))
	capSet := make(map[string]struct{}, len(e.Capabilities))
	for _, capability := range e.Capabilities {
		capSet[capability] = struct{}{}
	}
	preferenceBoost := 0
	for i, preference := range summary.PreferredCapabilities {
		if labelsSatisfied([]string{preference}, capSet) {
			preferenceBoost = len(summary.PreferredCapabilities) - i
			break
		}
	}
	effective := e.BasePriority
	adjustment := summary.RunPriority + preferenceBoost
	if adjustment > 0 {
		effective += min(adjustment, e.PriorityCeiling-e.BasePriority)
	} else {
		effective += max(adjustment, -e.BasePriority)
	}
	highest, err := s.HighestActiveExecutorCeiling(ctx, summary, time.Now().Add(-ExecutorRegistrationActiveWindow))
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	return ExecutorMembershipSnapshot{
		MembershipID: executorMembershipID(e.TokenPrefix, e.Name), WorkerID: e.Name,
		Kind: e.Kind, RegisteredBasePriority: e.BasePriority,
		Eligible: eligible, EffectivePriority: effective, HighestEligibleCeiling: highest,
		ActiveSlots: active, MaxConcurrent: e.MaxConcurrent,
	}, nil
}

// HighestActiveExecutorCeiling returns the largest operator ceiling among
// active registered executors that satisfy the same hard slot/resource filter.
func (s *Store) HighestActiveExecutorCeiling(ctx context.Context, summary ExecutorSchedulingSummary, activeAfter time.Time) (int, error) {
	executors, err := s.ListExecutors(ctx)
	if err != nil {
		return 0, err
	}
	highest := 0
	for _, e := range executors {
		if e.LastSeen.Before(activeAfter) {
			continue
		}
		_, eligible, err := s.executorEligible(ctx, e, summary)
		if err != nil {
			return 0, err
		}
		if eligible {
			highest = max(highest, e.PriorityCeiling)
		}
	}
	return highest, nil
}

func (s *Store) executorEligible(ctx context.Context, e Executor, summary ExecutorSchedulingSummary) (int, bool, error) {
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return 0, false, err
	}
	var active int
	var usedCores float64
	var usedMemory int64
	if err := s.queryRow(ctx, `
SELECT COUNT(*), COALESCE(SUM(claim_cores), 0), COALESCE(SUM(claim_memory_bytes), 0)
  FROM nodes WHERE claim_executor = ? AND claimed_by IS NOT NULL
   AND lease_expires_at >= ? AND `+nodeNotDone, e.Name, time.Now().UnixNano()).Scan(&active, &usedCores, &usedMemory); err != nil {
		return 0, false, err
	}
	capSet := make(map[string]struct{}, len(e.Capabilities))
	for _, capability := range e.Capabilities {
		capSet[capability] = struct{}{}
	}
	placementEligible := (summary.RequiredCoordinatorID == "" || summary.RequiredCoordinatorID == coordinatorID) &&
		(summary.RequiredLocation == "" || summary.RequiredLocation != "unknown" && summary.RequiredLocation == e.Location)
	eligible := placementEligible && summary.Slots == 1 && active+summary.Slots <= e.MaxConcurrent &&
		labelsSatisfied(summary.HardCapabilities, capSet) &&
		executorResourcesFit(e, usedCores, usedMemory, summary.Resources)
	return active, eligible, nil
}

func executorMembershipID(tokenPrefix, name string) string {
	digest := sha256.Sum256([]byte(tokenPrefix + "\x00" + name))
	return fmt.Sprintf("membership-%x", digest[:12])
}

func executorResourceDigest(resource ExecutorResource) string {
	raw, _ := json.Marshal(resource)
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func (s *Store) getExecutor(ctx context.Context, name string) (Executor, error) {
	var e Executor
	var caps []byte
	var seen int64
	var reported int
	err := s.queryRow(ctx, `
SELECT name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
       budget_cores, budget_memory_bytes, principal, last_seen,
       headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth
  FROM executors WHERE name = ?`, name).Scan(
		&e.Name, &e.TokenPrefix, &e.Kind, &e.Location, &caps, &e.BasePriority, &e.PriorityCeiling, &e.MaxConcurrent,
		&e.Budget.Cores, &e.Budget.MemoryBytes, &e.Principal, &seen,
		&reported, &e.Headroom.Cores, &e.Headroom.MemoryBytes, &e.QueueDepth)
	if errors.Is(err, sql.ErrNoRows) {
		return Executor{}, ErrNotFound
	}
	if err != nil {
		return Executor{}, err
	}
	_ = json.Unmarshal(caps, &e.Capabilities)
	if seen > 0 {
		e.LastSeen = time.Unix(0, seen)
	}
	e.HeadroomReported = reported != 0
	return e, nil
}

// ClaimReadyNodeForExecutorWithReservation claims only the named ready node,
// recomputes its scheduling resource digest, and persists the supplied
// reservation and slot binding. The offer layer must validate reservation
// liveness before calling it.
func (s *Store) ClaimReadyNodeForExecutorWithReservation(ctx context.Context, claimant ClaimIdentity, executorName, runID, nodeID, holderID string, lease time.Duration, reservationID string, slot int, resourceDigest string) (*Node, error) {
	if executorName == "" || runID == "" || nodeID == "" || holderID == "" || reservationID == "" || slot < 0 || resourceDigest == "" {
		return nil, ErrLockHeld
	}
	lease = clampNodeLease(lease)
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := s.claimReadyNodeForExecutorTx(ctx, tx, claimant, executorName, runID, nodeID, holderID, lease, reservationID, slot, resourceDigest)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return n, nil
}

func (s *Store) claimReadyNodeForExecutorTx(ctx context.Context, tx *storeTx, claimant ClaimIdentity, executorName, runID, nodeID, holderID string, lease time.Duration, reservationID string, slot int, resourceDigest string) (*Node, error) {
	var e Executor
	var caps []byte
	var reported int
	var seen int64
	err := tx.QueryRowContext(ctx, `
SELECT token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
       budget_cores, budget_memory_bytes, principal, last_seen,
       headroom_reported, headroom_cores, headroom_memory_bytes
  FROM executors WHERE name = ?`+s.forUpdate(), executorName).Scan(
		&e.TokenPrefix, &e.Kind, &e.Location, &caps, &e.BasePriority, &e.PriorityCeiling, &e.MaxConcurrent,
		&e.Budget.Cores, &e.Budget.MemoryBytes, &e.Principal, &seen,
		&reported, &e.Headroom.Cores, &e.Headroom.MemoryBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.TokenPrefix != claimant.TokenPrefix || claimant.TokenPrefix == "" || e.Principal != claimant.Principal {
		return nil, fmt.Errorf("%w: %s", ErrExecutorCredentialMismatch, executorName)
	}
	_ = json.Unmarshal(caps, &e.Capabilities)
	e.HeadroomReported = reported != 0

	now := time.Now()
	if !e.HeadroomReported || time.Unix(0, seen).Before(now.Add(-ExecutorRegistrationActiveWindow)) || slot >= e.MaxConcurrent {
		return nil, ErrNotFound
	}
	var duplicate int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM nodes
 WHERE claimed_by IS NOT NULL AND lease_expires_at >= ? AND `+nodeNotDone+`
   AND ((claim_executor = ? AND claim_slot = ?) OR claim_reservation = ?)`,
		now.UnixNano(), executorName, slot, reservationID).Scan(&duplicate); err != nil {
		return nil, err
	}
	if duplicate != 0 {
		return nil, ErrLockHeld
	}
	var active int
	var usedCores float64
	var usedMemory int64
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(claim_cores), 0), COALESCE(SUM(claim_memory_bytes), 0)
  FROM nodes
 WHERE claim_executor = ? AND claimed_by IS NOT NULL
   AND lease_expires_at >= ? AND `+nodeNotDone, executorName, now.UnixNano()).Scan(
		&active, &usedCores, &usedMemory); err != nil {
		return nil, err
	}
	if active >= e.MaxConcurrent {
		return nil, ErrNotFound
	}

	n := &Node{}
	err = scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL AND claimed_by IS NULL AND `+nodeNotDone+s.forUpdate(), runID, nodeID), n)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	capSet := make(map[string]struct{}, len(e.Capabilities))
	for _, capability := range e.Capabilities {
		capSet[capability] = struct{}{}
	}
	charge, err := s.executorNodeCharge(ctx, tx, n)
	if err != nil {
		return nil, err
	}
	if resourceDigest != executorResourceDigest(charge) {
		return nil, ErrLockHeld
	}
	if !labelsSatisfied(n.NeedsLabels, capSet) || !executorResourcesFit(e, usedCores, usedMemory, charge) {
		return nil, ErrNotFound
	}

	expires := now.Add(lease)
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	membershipID := executorMembershipID(e.TokenPrefix, executorName)
	result, err := tx.ExecContext(ctx, `
UPDATE nodes
   SET claimed_by = ?, claim_principal = ?, claim_token_prefix = ?,
       claim_executor = ?, claim_cores = ?, claim_memory_bytes = ?,
       claim_reservation = ?, claim_slot = ?, lease_expires_at = ?,
       coordinator_id = ?, claim_membership_id = ?, executor_kind = ?, executor_id = ?,
	   executor_location = ?, reservation_id = ?, claim_generation = claim_generation + 1,
	   required_coordinator_id = CASE WHEN required_coordinator_id = '' THEN ? ELSE required_coordinator_id END,
	   required_executor_location = CASE WHEN required_executor_location = '' THEN ? ELSE required_executor_location END
	 WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL
	   AND (required_coordinator_id = '' OR required_coordinator_id = ?)
	   AND (required_executor_location = '' OR (required_executor_location != 'unknown' AND required_executor_location = ?))`,
		holderID, e.Principal, claimant.TokenPrefix,
		executorName, charge.Cores, charge.MemoryBytes, reservationID, slot,
		expires.UnixNano(), coordinatorID, membershipID, e.Kind, executorName,
		e.Location, reservationID, coordinatorID, e.Location, n.RunID, n.NodeID, coordinatorID, e.Location)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, ErrLockHeld
	}
	n.ClaimedBy = holderID
	n.LeaseExpiresAt = &expires
	n.CoordinatorID = coordinatorID
	n.ClaimMembershipID = membershipID
	n.ExecutorKind = e.Kind
	n.ExecutorID = executorName
	n.ExecutorLocation = e.Location
	if n.RequiredCoordinatorID == "" {
		n.RequiredCoordinatorID = coordinatorID
	}
	if n.RequiredExecutorLocation == "" {
		n.RequiredExecutorLocation = e.Location
	}
	n.ReservationID = reservationID
	n.ClaimGeneration++
	event := executionAttributionEventFields(e.Kind, executorName, e.Location)
	event["claim_generation"] = n.ClaimGeneration
	if n.AvoidUntil != nil && n.AvoidUntil.After(now) {
		event["avoided_executor_kind"] = n.AvoidExecutorKind
		event["avoided_executor_name"] = n.AvoidExecutorID
		event["avoid_until"] = n.AvoidUntil
	}
	payload, _ := json.Marshal(event)
	if _, err := appendEventTx(ctx, tx, n.RunID, n.NodeID, "executor_selected", payload, now); err != nil {
		return nil, err
	}
	return n, nil
}

// ValidateExecutorClaimReservation proves that a live claim carries the
// expected principal-bound executor, opaque reservation, and physical slot.
func (s *Store) ValidateExecutorClaimReservation(ctx context.Context, claimant ClaimIdentity, runID, nodeID, executorName, reservationID string, slot int, resourceDigest string) error {
	var principal, tokenPrefix, gotExecutor, gotReservation string
	var gotSlot int
	var expires int64
	var cores float64
	var memory int64
	err := s.queryRow(ctx, `
SELECT claim_principal, claim_token_prefix, claim_executor, claim_reservation, claim_slot,
       claim_cores, claim_memory_bytes, COALESCE(lease_expires_at, 0)
  FROM nodes WHERE run_id = ? AND node_id = ? AND claimed_by IS NOT NULL`, runID, nodeID).Scan(
		&principal, &tokenPrefix, &gotExecutor, &gotReservation, &gotSlot, &cores, &memory, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if expires < time.Now().UnixNano() || principal != claimant.Principal || tokenPrefix != claimant.TokenPrefix ||
		gotExecutor != executorName || reservationID == "" || gotReservation != reservationID || gotSlot != slot ||
		resourceDigest == "" || resourceDigest != executorResourceDigest(ExecutorResource{Cores: cores, MemoryBytes: memory}) {
		return ErrLockHeld
	}
	return nil
}

func executorResourcesFit(e Executor, usedCores float64, usedMemory int64, charge ExecutorResource) bool {
	if e.Budget.Cores > 0 && usedCores+charge.Cores > e.Budget.Cores {
		return false
	}
	if e.Budget.MemoryBytes > 0 && usedMemory+charge.MemoryBytes > e.Budget.MemoryBytes {
		return false
	}
	if e.HeadroomReported && e.Headroom.Cores >= 0 && charge.Cores > e.Headroom.Cores {
		return false
	}
	if e.HeadroomReported && e.Headroom.MemoryBytes >= 0 && charge.MemoryBytes > e.Headroom.MemoryBytes {
		return false
	}
	return true
}

func (s *Store) executorNodeCharge(ctx context.Context, tx *storeTx, n *Node) (ExecutorResource, error) {
	var pipeline string
	var plan []byte
	if err := tx.QueryRowContext(ctx, `SELECT pipeline, plan_json FROM runs WHERE id = ?`, n.RunID).Scan(&pipeline, &plan); err != nil {
		return ExecutorResource{}, err
	}
	if pin := snapshotNodeResource(plan, n.NodeID); pin.Cores > 0 || pin.MemoryBytes > 0 {
		return pin, nil
	}
	row := tx.QueryRowContext(ctx, `
SELECT `+profileColumns+`
  FROM pipeline_profiles WHERE pipeline = ? AND node_id = ?`, pipeline, n.NodeID)
	profile, err := scanProfile(row, pipeline, n.NodeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ExecutorResource{}, err
	}
	if err == nil {
		if profile.PinnedCores > 0 || profile.PinnedMemoryBytes > 0 {
			return ExecutorResource{Cores: profile.PinnedCores, MemoryBytes: profile.PinnedMemoryBytes}, nil
		}
		if profile.SampleCount >= 3 && (profile.PeakCores > 0 || profile.CPUMeasured) {
			cores := profile.SustainedCores
			if cores <= 0 {
				cores = profile.PeakCores
			}
			return ExecutorResource{Cores: math.Max(cores, 0.1), MemoryBytes: profile.PeakMemoryBytes}, nil
		}
		cores := profile.PrevSustainedCores
		if cores <= 0 {
			cores = profile.PrevPeakCores
		}
		cores = math.Max(cores, 2*profile.FloorCores)
		memory := max(profile.PrevPeakMemoryBytes, 2*profile.FloorMemoryBytes)
		if cores > 0 || memory > 0 {
			return ExecutorResource{Cores: cores, MemoryBytes: memory}, nil
		}
	}
	return ExecutorResource{Cores: 1}, nil
}

func snapshotNodeResource(raw []byte, nodeID string) ExecutorResource {
	var snapshot struct {
		Nodes []struct {
			ID        string `json:"id"`
			Modifiers *struct {
				Cores       float64 `json:"res_cores"`
				MemoryBytes int64   `json:"res_memory_bytes"`
			} `json:"modifiers"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return ExecutorResource{}
	}
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID && node.Modifiers != nil {
			return ExecutorResource{Cores: node.Modifiers.Cores, MemoryBytes: node.Modifiers.MemoryBytes}
		}
	}
	return ExecutorResource{}
}

func snapshotNodePrefers(raw []byte, nodeID string) []string {
	var snapshot struct {
		Nodes []struct {
			ID        string `json:"id"`
			Modifiers *struct {
				Prefers []string `json:"prefers"`
			} `json:"modifiers"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil
	}
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID && node.Modifiers != nil {
			return append([]string(nil), node.Modifiers.Prefers...)
		}
	}
	return nil
}

func snapshotRunPriority(raw []byte) int {
	var snapshot struct {
		Priority int `json:"priority"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return 0
	}
	return max(-100, min(100, snapshot.Priority))
}
