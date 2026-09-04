package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// ErrExecutorCredentialMismatch means an executor is enrolled to another
// exact token prefix.
var ErrExecutorCredentialMismatch = errors.New("executor credential does not match enrollment")

// ErrExecutorEnrollmentLimit means a controller already has the maximum
// number of enrolled executors supported by one scheduling snapshot.
var ErrExecutorEnrollmentLimit = errors.New("executor enrollment limit reached: maximum 256 per controller")

// MaxEnrolledExecutors bounds one controller's executor scheduling work.
// Raising it does not require a schema change.
const MaxEnrolledExecutors = 256

// ExecutorRegistrationActiveWindow is how long the last successful heartbeat
// remains eligible before the executor is reported offline.
const ExecutorRegistrationActiveWindow = 2 * time.Minute

const (
	executorLocationLocal       = "local"
	executorLocationCloud       = "cloud"
	executorLocationUnknown     = "unknown"
	executorLocationCoordinator = "coordinator"
)

// ExecutorResource is a CPU and memory capacity or charge.
type ExecutorResource struct {
	Cores       float64 `json:"cores" yaml:"cores"`
	MemoryBytes int64   `json:"memory_bytes" yaml:"memory_bytes"`
}

// Executor is one administrator-enrolled execution membership.
type Executor struct {
	id               string
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
	if e.Location == "" {
		e.Location = executorLocationUnknown
	}
	coordinator := e.Kind == "direct" && e.Location == executorLocationCoordinator
	if e.Location != executorLocationLocal && e.Location != executorLocationCloud && e.Location != executorLocationUnknown && !coordinator {
		return errors.New("executor location must be local, cloud, or unknown")
	}
	for _, capability := range e.Capabilities {
		for _, value := range strings.Split(capability, ",") {
			value = strings.TrimSpace(value)
			if value == "local" || strings.HasPrefix(value, "location=") {
				return fmt.Errorf("executor capability %q uses reserved placement vocabulary", capability)
			}
		}
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
	if err := lockExecutorRegistryTx(ctx, tx, true); err != nil {
		return err
	}

	if err := enrollExecutorWith(ctx, tx, tokenPrefix, e, caps); err != nil {
		return err
	}
	return tx.Commit()
}

func enrollExecutorWith(ctx context.Context, tx *storeTx, tokenPrefix string, executor Executor, caps []byte) error {
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
		tokenPrefix, executor.Kind, executor.Location, caps, executor.BasePriority, executor.PriorityCeiling, executor.MaxConcurrent,
		executor.Budget.Cores, executor.Budget.MemoryBytes, executor.Principal, executor.Name)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		var enrolled int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM executors`).Scan(&enrolled); err != nil {
			return err
		}
		if enrolled >= MaxEnrolledExecutors {
			return ErrExecutorEnrollmentLimit
		}
		executorID, idErr := newOpaqueIdentity(executorIdentityPrefix)
		if idErr != nil {
			return idErr
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO executors
    (executor_id, name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
     budget_cores, budget_memory_bytes, principal, last_seen,
     headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 0)`,
			executorID, executor.Name, tokenPrefix, executor.Kind, executor.Location, caps, executor.BasePriority, executor.PriorityCeiling, executor.MaxConcurrent,
			executor.Budget.Cores, executor.Budget.MemoryBytes, executor.Principal)
		if err != nil {
			return err
		}
	}
	return nil
}

// ProvisionExecutor atomically mints one runner credential and binds its
// verifier to an operator-owned executor enrollment. The raw bearer is
// returned once and is not recoverable from the store.
func (s *Store) ProvisionExecutor(ctx context.Context, principal string, executor Executor, scopes []string, ttl time.Duration, now time.Time) (string, *Token, error) {
	if executor.Name == "" {
		return "", nil, errors.New("executor name is required")
	}
	if executor.BasePriority < 0 || executor.BasePriority > 100 || executor.PriorityCeiling < executor.BasePriority || executor.PriorityCeiling > 100 ||
		executor.MaxConcurrent < 1 || executor.Budget.Cores < 0 || math.IsNaN(executor.Budget.Cores) || math.IsInf(executor.Budget.Cores, 0) || executor.Budget.MemoryBytes < 0 {
		return "", nil, errors.New("executor enrollment has invalid priority or contribution limits")
	}
	caps, err := json.Marshal(executor.Capabilities)
	if err != nil {
		return "", nil, err
	}
	for attempt := 1; attempt <= mintAttempts; attempt++ {
		tx, err := s.beginTx(ctx)
		if err != nil {
			return "", nil, err
		}
		raw, tok, err := createTokenRow(ctx, tx, principal, TokenKindRunner, scopes, ttl, now)
		if err == nil {
			executor.Principal = principal
			err = enrollExecutorWith(ctx, tx, tok.Prefix, executor, caps)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err == nil {
			return raw, tok, nil
		}
		if attempt == mintAttempts || !isTokenPrefixCollision(err) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("executor credential prefix allocation exhausted")
}

// RollbackExecutorProvisioning revokes a newly provisioned credential and
// removes only the enrollment still bound to that exact credential.
func (s *Store) RollbackExecutorProvisioning(ctx context.Context, name, tokenPrefix string, now time.Time) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM executors WHERE name = ? AND token_prefix = ?`, name, tokenPrefix)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return err
		}
		return ErrExecutorCredentialMismatch
	}
	result, err = tx.ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE prefix = ? AND revoked_at IS NULL`, now.UTC().Unix(), tokenPrefix)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return err
		}
		return ErrExecutorCredentialMismatch
	}
	return tx.Commit()
}

// HeartbeatExecutor refreshes only liveness and measured headroom after the
// exact enrolled credential proves possession.
func (s *Store) HeartbeatExecutor(ctx context.Context, claimant ClaimIdentity, name string, headroom ExecutorResource, queueDepth int, now time.Time) error {
	if claimant.TokenPrefix == "" || headroom.Cores < 0 || math.IsNaN(headroom.Cores) || math.IsInf(headroom.Cores, 0) || headroom.MemoryBytes < 0 || queueDepth < 0 {
		return errors.New("executor heartbeat requires a credential and finite non-negative headroom")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockExecutorRegistryTx(ctx, tx, true); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE executors
   SET last_seen = ?, headroom_reported = 1, headroom_cores = ?, headroom_memory_bytes = ?, queue_depth = ?
 WHERE name = ? AND token_prefix = ? AND principal = ?`,
		now.UnixNano(), headroom.Cores, headroom.MemoryBytes, queueDepth, name, claimant.TokenPrefix, claimant.Principal)
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
	return tx.Commit()
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
SELECT executor_id, name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
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
		if err := rows.Scan(&e.id, &e.Name, &e.TokenPrefix, &e.Kind, &e.Location, &caps, &e.BasePriority, &e.PriorityCeiling, &e.MaxConcurrent,
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

// ResetExecutorLiveness makes every persisted enrollment ineligible until its
// exact credential heartbeats again. A foreground coordinator calls this
// before reconciling its narrower fleet allowlist.
func (s *Store) ResetExecutorLiveness(ctx context.Context) error {
	_, err := s.exec(ctx, `UPDATE executors
SET last_seen = 0, headroom_reported = 0, headroom_cores = 0,
    headroom_memory_bytes = 0, queue_depth = 0`)
	return err
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

// ExecutorMembershipSnapshot binds a registered executor to one controller
// authority without exposing the credential used to prove that membership.
type ExecutorMembershipSnapshot struct {
	MembershipID            string `json:"membership_id"`
	WorkerID                string `json:"worker_id"`
	Kind                    string `json:"kind"`
	RegisteredBasePriority  int    `json:"registered_base_priority"`
	Eligible                bool   `json:"eligible"`
	EffectivePriority       int    `json:"effective_priority"`
	HighestEligiblePriority int    `json:"highest_eligible_priority"`
	ActiveSlots             int    `json:"active_slots"`
	MaxConcurrent           int    `json:"max_concurrent"`
}

// ExecutorEligibilityPreview explains whether one configured executor can
// enter arbitration for a scheduling summary without exposing its credential.
type ExecutorEligibilityPreview struct {
	MembershipID    string `json:"membership_id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Location        string `json:"location"`
	Eligible        bool   `json:"eligible"`
	ExclusionReason string `json:"exclusion_reason,omitempty"`
	EffectiveScore  *int   `json:"effective_score,omitempty"`
	PriorityCeiling *int   `json:"priority_ceiling,omitempty"`
}

// PreviewExecutorEligibility evaluates every enrollment with the same filters
// used by claim-offer award revalidation. activeAfter defines online liveness.
func (s *Store) PreviewExecutorEligibility(ctx context.Context, summary ExecutorSchedulingSummary, activeAfter time.Time) ([]ExecutorEligibilityPreview, error) {
	executors, err := s.ListExecutors(ctx)
	if err != nil {
		return nil, err
	}
	if len(executors) > MaxEnrolledExecutors {
		return nil, ErrExecutorEnrollmentLimit
	}
	authorityID, err := s.controllerAuthorityID(ctx)
	if err != nil {
		return nil, err
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	usage, err := s.loadExecutorUsage(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make([]ExecutorEligibilityPreview, 0, len(executors))
	for _, e := range executors {
		used := usage.ByExecutor[e.Name]
		reason := executorExclusionReason(e, summary, used.Active, used.Cores, used.MemoryBytes, activeAfter, coordinatorID)
		preview := ExecutorEligibilityPreview{
			MembershipID: executorMembershipID(authorityID, e.id), Name: e.Name,
			Kind: e.Kind, Location: e.Location, Eligible: reason == "", ExclusionReason: reason,
		}
		if preview.Eligible {
			score, ceiling := executorEffectivePriority(e, summary), e.PriorityCeiling
			preview.EffectiveScore, preview.PriorityCeiling = &score, &ceiling
		}
		out = append(out, preview)
	}
	return out, nil
}

// SchedulingSummary resolves the resource charge and hard/preferred labels
// for a persisted node without changing claim or ready-queue state.
func (s *Store) SchedulingSummary(ctx context.Context, runID, nodeID string) (ExecutorSchedulingSummary, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return ExecutorSchedulingSummary{}, err
	}
	defer func() { _ = tx.Rollback() }()
	summary, err := s.schedulingSummaryTx(ctx, tx, runID, nodeID)
	if err != nil {
		return ExecutorSchedulingSummary{}, err
	}
	return summary, nil
}

func (s *Store) schedulingSummaryTx(ctx context.Context, tx *storeTx, runID, nodeID string) (ExecutorSchedulingSummary, error) {
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
	now := time.Now()
	membership, err := s.resolveExecutorMembership(ctx, claimant, executorName, summary, now)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	highest, err := s.HighestActiveExecutorPriority(ctx, summary, now.Add(-ExecutorRegistrationActiveWindow))
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	membership.HighestEligiblePriority = highest
	return membership, nil
}

// safety: offer admission consumes the target recorded at round creation, so resolving
// one membership here must not repeat that fleet-wide work.
func (s *Store) resolveExecutorMembership(ctx context.Context, claimant ClaimIdentity, executorName string, summary ExecutorSchedulingSummary, now time.Time) (ExecutorMembershipSnapshot, error) {
	e, err := s.getExecutor(ctx, executorName)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	if e.TokenPrefix != claimant.TokenPrefix || claimant.TokenPrefix == "" || e.Principal != claimant.Principal {
		return ExecutorMembershipSnapshot{}, fmt.Errorf("%w: %s", ErrExecutorCredentialMismatch, executorName)
	}
	activeAfter := now.Add(-ExecutorRegistrationActiveWindow)
	active, reason, err := s.executorEligibility(ctx, e, summary, now, activeAfter)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	eligible := reason == ""
	authorityID, err := s.controllerAuthorityID(ctx)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	return ExecutorMembershipSnapshot{
		MembershipID: executorMembershipID(authorityID, e.id), WorkerID: e.Name,
		Kind: e.Kind, RegisteredBasePriority: e.BasePriority,
		Eligible: eligible, EffectivePriority: executorEffectivePriority(e, summary),
		ActiveSlots: active, MaxConcurrent: e.MaxConcurrent,
	}, nil
}

func (s *Store) resolveExecutorMembershipTx(ctx context.Context, tx *storeTx, claimant ClaimIdentity, executorName string, summary ExecutorSchedulingSummary, now time.Time) (ExecutorMembershipSnapshot, error) {
	e, err := s.getExecutorTx(ctx, tx, executorName)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	if e.TokenPrefix != claimant.TokenPrefix || claimant.TokenPrefix == "" || e.Principal != claimant.Principal {
		return ExecutorMembershipSnapshot{}, fmt.Errorf("%w: %s", ErrExecutorCredentialMismatch, executorName)
	}
	activeAfter := now.Add(-ExecutorRegistrationActiveWindow)
	active, reason, err := s.executorEligibilityTx(ctx, tx, e, summary, now, activeAfter)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	eligible := reason == ""
	authorityID, err := controllerAuthorityIDTx(ctx, tx)
	if err != nil {
		return ExecutorMembershipSnapshot{}, err
	}
	return ExecutorMembershipSnapshot{
		MembershipID: executorMembershipID(authorityID, e.id), WorkerID: e.Name,
		Kind: e.Kind, RegisteredBasePriority: e.BasePriority,
		Eligible: eligible, EffectivePriority: executorEffectivePriority(e, summary),
		ActiveSlots: active, MaxConcurrent: e.MaxConcurrent,
	}, nil
}

func executorMembershipFromSnapshot(e Executor, claimant ClaimIdentity, summary ExecutorSchedulingSummary, used executorUsage, authorityID, coordinatorID string, activeAfter time.Time) (ExecutorMembershipSnapshot, error) {
	if e.TokenPrefix != claimant.TokenPrefix || claimant.TokenPrefix == "" || e.Principal != claimant.Principal {
		return ExecutorMembershipSnapshot{}, fmt.Errorf("%w: %s", ErrExecutorCredentialMismatch, e.Name)
	}
	reason := executorExclusionReason(e, summary, used.Active, used.Cores, used.MemoryBytes, activeAfter, coordinatorID)
	return ExecutorMembershipSnapshot{
		MembershipID: executorMembershipID(authorityID, e.id), WorkerID: e.Name,
		Kind: e.Kind, RegisteredBasePriority: e.BasePriority,
		Eligible: reason == "", EffectivePriority: executorEffectivePriority(e, summary),
		ActiveSlots: used.Active, MaxConcurrent: e.MaxConcurrent,
	}, nil
}

// HighestActiveExecutorPriority returns the largest attainable score among
// active registered executors that satisfy the same hard slot/resource filter.
func (s *Store) HighestActiveExecutorPriority(ctx context.Context, summary ExecutorSchedulingSummary, activeAfter time.Time) (int, error) {
	executors, err := s.ListExecutors(ctx)
	if err != nil {
		return 0, err
	}
	if len(executors) > MaxEnrolledExecutors {
		return 0, ErrExecutorEnrollmentLimit
	}
	highest := 0
	now := time.Now()
	usage, err := s.loadExecutorUsage(ctx, now)
	if err != nil {
		return 0, err
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return 0, err
	}
	for _, e := range executors {
		used := usage.ByExecutor[e.Name]
		reason := executorExclusionReason(e, summary, used.Active, used.Cores, used.MemoryBytes, activeAfter, coordinatorID)
		if reason == "" {
			highest = max(highest, executorEffectivePriority(e, summary))
		}
	}
	return highest, nil
}

func (s *Store) highestActiveExecutorPriorityTx(ctx context.Context, tx *storeTx, summary ExecutorSchedulingSummary, activeAfter, now time.Time) (int, error) {
	executors, err := loadExecutorsForSchedulingTx(ctx, tx, activeAfter)
	if err != nil {
		return 0, err
	}
	usage, err := loadExecutorUsageTx(ctx, tx, now)
	if err != nil {
		return 0, err
	}
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	highest := 0
	for _, e := range executors {
		used := usage.ByExecutor[e.Name]
		reason := executorExclusionReason(e, summary, used.Active, used.Cores, used.MemoryBytes, activeAfter, coordinatorID)
		if reason == "" {
			highest = max(highest, executorEffectivePriority(e, summary))
		}
	}
	return highest, nil
}

type executorUsage struct {
	Active      int
	Cores       float64
	MemoryBytes int64
	Slots       map[int]struct{}
}

type executorUsageSnapshot struct {
	ByExecutor   map[string]executorUsage
	Reservations map[string]struct{}
}

const executorUsageQuery = `
SELECT claim_executor, claim_slot, claim_reservation, claim_cores, claim_memory_bytes
  FROM nodes
 WHERE claimed_by IS NOT NULL AND lease_expires_at >= ? AND ` + nodeNotDone

func (s *Store) loadExecutorUsage(ctx context.Context, now time.Time) (executorUsageSnapshot, error) {
	rows, err := s.query(ctx, executorUsageQuery, now.UnixNano())
	if err != nil {
		return executorUsageSnapshot{}, err
	}
	return scanExecutorUsage(rows)
}

func loadExecutorUsageTx(ctx context.Context, tx *storeTx, now time.Time) (executorUsageSnapshot, error) {
	rows, err := tx.QueryContext(ctx, executorUsageQuery, now.UnixNano())
	if err != nil {
		return executorUsageSnapshot{}, err
	}
	return scanExecutorUsage(rows)
}

func scanExecutorUsage(rows *sql.Rows) (executorUsageSnapshot, error) {
	defer func() { _ = rows.Close() }()
	out := executorUsageSnapshot{
		ByExecutor:   map[string]executorUsage{},
		Reservations: map[string]struct{}{},
	}
	for rows.Next() {
		var name, reservation string
		var slot int
		var cores float64
		var memory int64
		if err := rows.Scan(&name, &slot, &reservation, &cores, &memory); err != nil {
			return executorUsageSnapshot{}, err
		}
		if reservation != "" {
			out.Reservations[reservation] = struct{}{}
		}
		if name == "" {
			continue
		}
		used := out.ByExecutor[name]
		used.Active++
		used.Cores += cores
		used.MemoryBytes += memory
		if used.Slots == nil {
			used.Slots = map[int]struct{}{}
		}
		used.Slots[slot] = struct{}{}
		out.ByExecutor[name] = used
	}
	return out, rows.Err()
}

func loadExecutorsForSchedulingTx(ctx context.Context, tx *storeTx, activeAfter time.Time) ([]Executor, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT executor_id, name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
       budget_cores, budget_memory_bytes, principal, last_seen,
       headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth
  FROM executors WHERE last_seen >= ? ORDER BY name LIMIT ?`, activeAfter.UnixNano(), MaxEnrolledExecutors+1)
	if err != nil {
		return nil, err
	}
	out, err := scanExecutors(rows)
	if err != nil {
		return nil, err
	}
	if len(out) > MaxEnrolledExecutors {
		return nil, ErrExecutorEnrollmentLimit
	}
	return out, nil
}

func ensureExecutorCardinalityTx(ctx context.Context, tx *storeTx) error {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM executors LIMIT ?`, MaxEnrolledExecutors+1)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count > MaxEnrolledExecutors {
		return ErrExecutorEnrollmentLimit
	}
	return nil
}

func loadExecutorsByNameTx(ctx context.Context, tx *storeTx, names []string) (map[string]Executor, error) {
	names = canonicalExecutorNames(names...)
	if len(names) == 0 {
		return map[string]Executor{}, nil
	}
	if len(names) > MaxEnrolledExecutors {
		return nil, ErrExecutorEnrollmentLimit
	}
	args := make([]any, len(names))
	for i := range names {
		args[i] = names[i]
	}
	rows, err := tx.QueryContext(ctx, `
SELECT executor_id, name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
       budget_cores, budget_memory_bytes, principal, last_seen,
       headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth
  FROM executors WHERE name IN (`+strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")+`) ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	items, err := scanExecutors(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Executor, len(items))
	for _, e := range items {
		out[e.Name] = e
	}
	return out, nil
}

func scanExecutors(rows *sql.Rows) ([]Executor, error) {
	defer func() { _ = rows.Close() }()
	var out []Executor
	for rows.Next() {
		var e Executor
		var caps []byte
		var seen int64
		var reported int
		if err := rows.Scan(&e.id, &e.Name, &e.TokenPrefix, &e.Kind, &e.Location, &caps,
			&e.BasePriority, &e.PriorityCeiling, &e.MaxConcurrent,
			&e.Budget.Cores, &e.Budget.MemoryBytes, &e.Principal, &seen,
			&reported, &e.Headroom.Cores, &e.Headroom.MemoryBytes, &e.QueueDepth); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(caps, &e.Capabilities)
		e.LastSeen = time.Unix(0, seen)
		e.HeadroomReported = reported != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func executorEffectivePriority(e Executor, summary ExecutorSchedulingSummary) int {
	capSet := executorTrustSet(e)
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
		return effective + min(adjustment, e.PriorityCeiling-e.BasePriority)
	}
	return effective + max(adjustment, -e.BasePriority)
}

func (s *Store) executorEligibility(ctx context.Context, e Executor, summary ExecutorSchedulingSummary, now, activeAfter time.Time) (int, string, error) {
	var active int
	var usedCores float64
	var usedMemory int64
	if err := s.queryRow(ctx, `
SELECT COUNT(*), COALESCE(SUM(claim_cores), 0), COALESCE(SUM(claim_memory_bytes), 0)
  FROM nodes WHERE claim_executor = ? AND claimed_by IS NOT NULL
   AND lease_expires_at >= ? AND `+nodeNotDone, e.Name, now.UnixNano()).Scan(&active, &usedCores, &usedMemory); err != nil {
		return 0, "", err
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return 0, "", err
	}
	return active, executorExclusionReason(e, summary, active, usedCores, usedMemory, activeAfter, coordinatorID), nil
}

func (s *Store) executorEligibilityTx(ctx context.Context, tx *storeTx, e Executor, summary ExecutorSchedulingSummary, now, activeAfter time.Time) (int, string, error) {
	var active int
	var usedCores float64
	var usedMemory int64
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(claim_cores), 0), COALESCE(SUM(claim_memory_bytes), 0)
  FROM nodes WHERE claim_executor = ? AND claimed_by IS NOT NULL
   AND lease_expires_at >= ? AND `+nodeNotDone, e.Name, now.UnixNano()).Scan(&active, &usedCores, &usedMemory); err != nil {
		return 0, "", err
	}
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return 0, "", err
	}
	return active, executorExclusionReason(e, summary, active, usedCores, usedMemory, activeAfter, coordinatorID), nil
}

func executorExclusionReason(e Executor, summary ExecutorSchedulingSummary, active int, usedCores float64, usedMemory int64, activeAfter time.Time, coordinatorID string) string {
	if e.LastSeen.Before(activeAfter) {
		return "offline"
	}
	if summary.RequiredCoordinatorID != "" && summary.RequiredCoordinatorID != coordinatorID {
		return "trusted_placement"
	}
	if summary.RequiredLocation != "" && (summary.RequiredLocation == executorLocationUnknown || summary.RequiredLocation != e.Location) {
		return "trusted_placement"
	}
	capSet := executorTrustSet(e)
	placementMissing, capabilityMissing := false, false
	for _, term := range summary.HardCapabilities {
		if term == "" || labelTermSatisfied(term, capSet) {
			continue
		}
		if executorTermContainsPlacement(term) {
			placementMissing = true
		} else {
			capabilityMissing = true
		}
	}
	if placementMissing {
		return "trusted_placement"
	}
	if capabilityMissing {
		return "hard_capability"
	}
	if summary.Slots != 1 || active+summary.Slots > e.MaxConcurrent {
		return "slot_limit"
	}
	if (e.Budget.Cores > 0 && usedCores+summary.Resources.Cores > e.Budget.Cores) ||
		(e.Budget.MemoryBytes > 0 && usedMemory+summary.Resources.MemoryBytes > e.Budget.MemoryBytes) {
		return "resource_budget"
	}
	if (e.HeadroomReported && e.Headroom.Cores >= 0 && summary.Resources.Cores > e.Headroom.Cores) ||
		(e.HeadroomReported && e.Headroom.MemoryBytes >= 0 && summary.Resources.MemoryBytes > e.Headroom.MemoryBytes) {
		return "headroom"
	}
	return ""
}

func executorTermContainsPlacement(term string) bool {
	for _, value := range strings.Split(term, ",") {
		value = strings.TrimSpace(value)
		if value == "local" || strings.HasPrefix(value, "location=") {
			return true
		}
	}
	return false
}

func executorTrustSet(e Executor) map[string]struct{} {
	trusted := make(map[string]struct{}, len(e.Capabilities)+1)
	for _, capability := range e.Capabilities {
		if capability != "local" && !strings.HasPrefix(capability, "location=") {
			trusted[capability] = struct{}{}
		}
	}
	if e.Location == executorLocationLocal || e.Location == executorLocationCloud {
		trusted["location="+e.Location] = struct{}{}
	}
	// safety: coordinator placement is never grantable to an enrolled helper.
	delete(trusted, "location="+executorLocationCoordinator)
	return trusted
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
SELECT executor_id, name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
       budget_cores, budget_memory_bytes, principal, last_seen,
       headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth
  FROM executors WHERE name = ?`, name).Scan(
		&e.id, &e.Name, &e.TokenPrefix, &e.Kind, &e.Location, &caps, &e.BasePriority, &e.PriorityCeiling, &e.MaxConcurrent,
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

const executorRegistryAdvisoryLock = "sparkwing/executor-registry"
const executorEligibilityAdvisoryLock = "sparkwing/executor-eligibility"

// safety: PostgreSQL mutations share the eligibility lock; MarkNodeReady takes
// it exclusively, so ordinary mutations overlap but no availability change can
// cross the round-opening snapshot. Executor changes use the registry lock;
// allocation paths also fence the affected executor row.
func lockExecutorEligibilityTx(ctx context.Context, tx *storeTx, exclusive bool) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	lockFunction := "pg_advisory_xact_lock_shared"
	if exclusive {
		lockFunction = "pg_advisory_xact_lock"
	}
	_, err := tx.ExecContext(ctx, `SELECT `+lockFunction+`(hashtext(?))`, executorEligibilityAdvisoryLock)
	return err
}

func (s *Store) withExecutorEligibilityTx(ctx context.Context, mutate func(*storeTx) error) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return err
	}
	if err := mutate(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func lockExecutorRegistryTx(ctx context.Context, tx *storeTx, exclusive bool) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	lockFunction := "pg_advisory_xact_lock_shared"
	if exclusive {
		lockFunction = "pg_advisory_xact_lock"
	}
	_, err := tx.ExecContext(ctx, `SELECT `+lockFunction+`(hashtext(?))`, executorRegistryAdvisoryLock)
	return err
}

func canonicalExecutorNames(names ...string) []string {
	ordered := append([]string(nil), names...)
	sort.Strings(ordered)
	out := ordered[:0]
	for _, name := range ordered {
		if name == "" || len(out) > 0 && out[len(out)-1] == name {
			continue
		}
		out = append(out, name)
	}
	return out
}

func lockExecutorRowsCanonicalTx(ctx context.Context, tx *storeTx, names ...string) error {
	return lockExecutorRowsTx(ctx, tx, true, names...)
}

func lockExistingExecutorRowsCanonicalTx(ctx context.Context, tx *storeTx, names ...string) error {
	return lockExecutorRowsTx(ctx, tx, false, names...)
}

func lockExecutorRowsTx(ctx context.Context, tx *storeTx, requireAll bool, names ...string) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	ordered := canonicalExecutorNames(names...)
	if len(ordered) == 0 {
		return nil
	}
	if len(ordered) > MaxEnrolledExecutors {
		return ErrExecutorEnrollmentLimit
	}
	args := make([]any, len(ordered))
	for i := range ordered {
		args[i] = ordered[i]
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM executors WHERE name IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ordered)), ",")+`) ORDER BY name`+tx.forUpdate(), args...)
	if err != nil {
		return err
	}
	locked := 0
	for rows.Next() {
		locked++
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if requireAll && locked != len(ordered) {
		return ErrNotFound
	}
	return nil
}

func lockAllExecutorRowsCanonicalTx(ctx context.Context, tx *storeTx) error {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM executors ORDER BY name LIMIT ?`, MaxEnrolledExecutors+1)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		names = append(names, name)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if len(names) > MaxEnrolledExecutors {
		return ErrExecutorEnrollmentLimit
	}
	return lockExecutorRowsCanonicalTx(ctx, tx, names...)
}

func (s *Store) getExecutorTx(ctx context.Context, tx *storeTx, name string) (Executor, error) {
	var e Executor
	var caps []byte
	var seen int64
	var reported int
	err := tx.QueryRowContext(ctx, `
SELECT executor_id, name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
       budget_cores, budget_memory_bytes, principal, last_seen,
       headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth
  FROM executors WHERE name = ?`, name).Scan(
		&e.id, &e.Name, &e.TokenPrefix, &e.Kind, &e.Location, &caps, &e.BasePriority, &e.PriorityCeiling, &e.MaxConcurrent,
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
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return nil, err
	}
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
	n := &Node{}
	err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL AND claimed_by IS NULL AND `+nodeNotDone+tx.forUpdate(), runID, nodeID), n)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var lockedRun string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE id = ?`+tx.forUpdate(), runID).Scan(&lockedRun); err != nil {
		return nil, err
	}
	if err := lockExecutorRegistryTx(ctx, tx, false); err != nil {
		return nil, err
	}
	if err := lockExecutorRowsCanonicalTx(ctx, tx, executorName); err != nil {
		return nil, err
	}
	var e Executor
	var caps []byte
	var reported int
	var seen int64
	err = tx.QueryRowContext(ctx, `
SELECT executor_id, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling, max_concurrent,
       budget_cores, budget_memory_bytes, principal, last_seen,
       headroom_reported, headroom_cores, headroom_memory_bytes
  FROM executors WHERE name = ?`, executorName).Scan(
		&e.id, &e.TokenPrefix, &e.Kind, &e.Location, &caps, &e.BasePriority, &e.PriorityCeiling, &e.MaxConcurrent,
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
	if seen > 0 {
		e.LastSeen = time.Unix(0, seen)
	}

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

	charge, err := s.executorNodeCharge(ctx, tx, n)
	if err != nil {
		return nil, err
	}
	if resourceDigest != executorResourceDigest(charge) {
		return nil, ErrLockHeld
	}
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	summary, err := s.schedulingSummaryTx(ctx, tx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	if executorExclusionReason(e, summary, active, usedCores, usedMemory, now.Add(-ExecutorRegistrationActiveWindow), coordinatorID) != "" {
		return nil, ErrNotFound
	}
	authorityID, err := controllerAuthorityIDTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	expires := now.Add(lease)
	membershipID := executorMembershipID(authorityID, e.id)
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
		expires.UnixNano(), coordinatorID, membershipID, e.Kind, e.id,
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
	n.ExecutorID = e.id
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
		if n.AvoidExecutorID == e.id {
			event["avoided_executor_name"] = executorName
		}
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
