package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
)

const (
	nodeClaimOfferWindow            = 5 * time.Second
	executorOfferLiveness           = 2 * time.Second
	executorPrepareCandidateLimit   = 128
	executorPrepareNeedsJSONMaxSize = 16 << 10
	executorPreparePlanJSONMaxSize  = executionpolicy.MaxEncodedPolicyBytes
)

var errExecutorOfferLimit = errors.New("executor scheduling limit exceeded: maximum 256 offers per node")

type executorPrepareCursor struct {
	readyAt       int64
	runID, nodeID string
}

type executorPrepareCandidate struct {
	executorPrepareCursor
	needsJSON                                              []byte
	requiredCoordinatorID, requiredLocation                string
	avoidCoordinatorID, avoidExecutorKind, avoidExecutorID string
	avoidUntil, offerStarted                               sql.NullInt64
	policyVersion, bodyProtocol, offerPriorityTarget       int
	supervisorJSON, bodyJSON                               []byte
	supervisorRequirementsHash, bodyRequirementsHash       string
}

type executorPreparePlan struct {
	pipeline string
	raw      []byte
}

type executorPrepareRange uint8

const (
	executorPrepareFromStart executorPrepareRange = iota
	executorPrepareAfterCursor
	executorPrepareThroughCursor
)

// ExecutorClaimPreparation is a controller-owned preview of one sealed node.
// Prepare returns it with ErrBodyAttestationRequired and never reserves capacity.
type ExecutorClaimPreparation struct {
	Summary       ExecutorSchedulingSummary  `json:"summary"`
	Membership    ExecutorMembershipSnapshot `json:"membership"`
	OfferDeadline *time.Time                 `json:"offer_deadline,omitempty"`
}

// ExecutorClaimOffer describes an attested reservation submission bound to an
// exact preparation digest. OfferExecutorClaim rejects unattested submissions.
type ExecutorClaimOffer struct {
	ExecutorName   string
	HolderID       string
	RunID          string
	NodeID         string
	ReservationID  string
	ResourceDigest string
	Slot           int
	Lease          time.Duration
}

// ExecutorClaimOfferResult describes an attested offer outcome. Unattested
// offers return ErrBodyAttestationRequired before reservation or arbitration.
type ExecutorClaimOfferResult struct {
	Node    *Node
	Pending bool
}

// ExecutorClaimRoundResult reports whether the coordinator may take fallback
// ownership or must keep waiting for the five-second round.
type ExecutorClaimRoundResult struct {
	Revoked bool
	Pending bool
}

type executorOfferEvent struct {
	ExecutorName          string           `json:"executor_name,omitempty"`
	ExecutorKind          string           `json:"executor_kind,omitempty"`
	ExecutorLocation      string           `json:"executor_location,omitempty"`
	BasePriority          int              `json:"base_priority"`
	EffectivePriority     int              `json:"effective_priority"`
	PriorityTarget        int              `json:"priority_target"`
	HardCapabilities      []string         `json:"hard_capabilities,omitempty"`
	PreferredCapabilities []string         `json:"preferred_capabilities,omitempty"`
	Resources             ExecutorResource `json:"resources"`
	Slots                 int              `json:"slots"`
	Slot                  *int             `json:"slot,omitempty"`
	Reason                string           `json:"reason,omitempty"`
	Outcome               string           `json:"outcome,omitempty"`
}

func executorOfferEventFor(summary ExecutorSchedulingSummary, membership ExecutorMembershipSnapshot, location string, target, slot int) executorOfferEvent {
	return executorOfferEvent{
		ExecutorName: membership.WorkerID,
		ExecutorKind: membership.Kind, ExecutorLocation: location,
		BasePriority: membership.RegisteredBasePriority, EffectivePriority: membership.EffectivePriority,
		PriorityTarget: target, HardCapabilities: summary.HardCapabilities,
		PreferredCapabilities: summary.PreferredCapabilities, Resources: summary.Resources, Slots: summary.Slots,
		Slot: offerSlot(slot),
	}
}

func offerSlot(slot int) *int { return &slot }

// PrepareNextExecutorClaim identifies a sealed compatible node without changing
// queue or claim state. It returns its preview with ErrBodyAttestationRequired.
func (s *Store) PrepareNextExecutorClaim(ctx context.Context, claimant ClaimIdentity, executorName string) (*ExecutorClaimPreparation, error) {
	return s.prepareNextExecutorClaim(ctx, claimant, executorName, "")
}

// PrepareExecutorClaimForRun restricts preparation to one coordinator-owned
// run. It keeps a foreground authority from exposing unrelated local work.
func (s *Store) PrepareExecutorClaimForRun(ctx context.Context, claimant ClaimIdentity, executorName, runID string) (*ExecutorClaimPreparation, error) {
	if runID == "" {
		return nil, errors.New("executor claim preparation requires a run")
	}
	return s.prepareNextExecutorClaim(ctx, claimant, executorName, runID)
}

func (s *Store) prepareNextExecutorClaim(ctx context.Context, claimant ClaimIdentity, executorName, runID string) (*ExecutorClaimPreparation, error) {
	executors, err := s.ListExecutors(ctx)
	if err != nil {
		return nil, err
	}
	if len(executors) > MaxEnrolledExecutors {
		return nil, ErrExecutorEnrollmentLimit
	}
	var executor Executor
	for _, candidate := range executors {
		if candidate.Name == executorName {
			executor = candidate
			break
		}
	}
	if executor.Name == "" {
		return nil, ErrNotFound
	}
	if claimant.TokenPrefix == "" || executor.TokenPrefix != claimant.TokenPrefix || executor.Principal != claimant.Principal {
		return nil, ErrExecutorCredentialMismatch
	}
	runtimeReport, err := s.executorRuntimeReport(ctx, executorName)
	if err != nil {
		return nil, err
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return nil, err
	}
	authorityID, err := s.controllerAuthorityID(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	usage, err := s.loadExecutorUsage(ctx, now)
	if err != nil {
		return nil, err
	}
	activeAfter := now.Add(-ExecutorRegistrationActiveWindow)
	cursor := s.loadExecutorPrepareCursor(executorName, runID)
	page, err := s.loadExecutorPrepareCandidates(ctx, runID, executorName, now, cursor)
	if err != nil {
		return nil, err
	}
	if len(page) == 0 {
		return nil, ErrNotFound
	}

	hardEligible := make([]executorPrepareCandidate, 0, len(page))
	for _, item := range page {
		var needs []string
		if len(item.needsJSON) != 0 {
			err = json.Unmarshal(item.needsJSON, &needs)
		}
		if err != nil {
			return nil, err
		}
		summary := ExecutorSchedulingSummary{
			RunID: item.runID, NodeID: item.nodeID, HardCapabilities: needs, Slots: 1,
			RequiredCoordinatorID: item.requiredCoordinatorID, RequiredLocation: item.requiredLocation,
		}
		if executorHardExclusionReason(executor, summary, coordinatorID) == "" {
			hardEligible = append(hardEligible, item)
		}
	}
	plans, err := s.loadExecutorPreparePlans(ctx, hardEligible)
	if err != nil {
		return nil, err
	}
	profiles, err := s.loadExecutorPrepareProfiles(ctx, hardEligible, plans)
	if err != nil {
		return nil, err
	}
	var runtimeRefusal error
	for _, item := range hardEligible {
		plan, ok := plans[item.runID]
		if !ok {
			continue
		}
		var needs []string
		if err := json.Unmarshal(item.needsJSON, &needs); err != nil && len(item.needsJSON) != 0 {
			return nil, err
		}
		charge := executorNodeChargeFromSnapshot(plan.raw, item.nodeID, profiles[executorPrepareProfileKey(plan.pipeline, item.nodeID)])
		summary := ExecutorSchedulingSummary{
			RunID: item.runID, NodeID: item.nodeID, HardCapabilities: needs,
			PreferredCapabilities: snapshotNodePrefers(plan.raw, item.nodeID),
			Resources:             charge, ResourceDigest: executorResourceDigest(charge), Slots: 1,
			RunPriority: snapshotRunPriority(plan.raw), RequiredCoordinatorID: item.requiredCoordinatorID,
			RequiredLocation: item.requiredLocation,
		}
		membership, err := executorMembershipFromSnapshot(executor, claimant, summary, usage.ByExecutor[executorName],
			authorityID, coordinatorID, activeAfter)
		if err != nil {
			return nil, err
		}
		if !membership.Eligible {
			continue
		}
		if item.avoidUntil.Valid && time.Unix(0, item.avoidUntil.Int64).After(now) &&
			item.avoidCoordinatorID == coordinatorID && item.avoidExecutorKind == executor.Kind && item.avoidExecutorID == executor.id {
			if hasAlternateEligibleExecutorSnapshot(executors, usage, executor.Name, summary, activeAfter, coordinatorID) {
				continue
			}
		}
		if err := executionpolicy.CheckRuntimeCompatibilityMetadata(
			item.policyVersion, item.bodyProtocol,
			item.supervisorJSON, item.supervisorRequirementsHash,
			item.bodyJSON, item.bodyRequirementsHash, runtimeReport,
		); err != nil {
			if isExecutorRuntimeRefusal(err) {
				if runtimeRefusal == nil {
					runtimeRefusal = err
				}
				continue
			}
			return nil, err
		}
		membership.HighestEligiblePriority = item.offerPriorityTarget
		preparation := &ExecutorClaimPreparation{Summary: summary, Membership: membership}
		if item.offerStarted.Valid {
			deadline := time.Unix(0, item.offerStarted.Int64).Add(nodeClaimOfferWindow)
			preparation.OfferDeadline = &deadline
		}
		record := &nodeRecord{}
		if err := scanNodeRow(s.queryRow(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes WHERE run_id = ? AND node_id = ?`, item.runID, item.nodeID), record); err != nil {
			return nil, err
		}
		policy, present, err := policyForNodeExecution(record)
		if err != nil || !present {
			if err == nil {
				err = errExecutionPolicyInvalid
			}
			return nil, err
		}
		if err := executionpolicy.CheckRuntimeCompatibility(policy, runtimeReport); err != nil {
			if isExecutorRuntimeRefusal(err) {
				if runtimeRefusal == nil {
					runtimeRefusal = err
				}
				continue
			}
			return nil, err
		}
		binding, err := claimBindingForNodeExecution(record)
		if err != nil {
			return nil, err
		}
		if err := executionpolicy.StorePreparation(ctx, binding); err != nil {
			return nil, err
		}
		s.storeExecutorPrepareCursor(executorName, runID, item.executorPrepareCursor)
		return preparation, &executionpolicy.BodyAttestationRequiredError{RunID: item.runID, NodeID: item.nodeID}
	}
	s.storeExecutorPrepareCursor(executorName, runID, page[len(page)-1].executorPrepareCursor)
	if runtimeRefusal != nil {
		return nil, runtimeRefusal
	}
	return nil, ErrNotFound
}

func (s *Store) loadExecutorPrepareCandidates(ctx context.Context, runID, executorName string, now time.Time,
	cursor executorPrepareCursor,
) ([]executorPrepareCandidate, error) {
	if cursor == (executorPrepareCursor{}) {
		return s.loadExecutorPrepareCandidateRange(ctx, runID, executorName, now,
			executorPrepareFromStart, cursor, executorPrepareCandidateLimit)
	}
	page, err := s.loadExecutorPrepareCandidateRange(ctx, runID, executorName, now,
		executorPrepareAfterCursor, cursor, executorPrepareCandidateLimit)
	if err != nil || len(page) == executorPrepareCandidateLimit {
		return page, err
	}
	wrapped, err := s.loadExecutorPrepareCandidateRange(ctx, runID, executorName, now,
		executorPrepareThroughCursor, cursor, executorPrepareCandidateLimit-len(page))
	return append(page, wrapped...), err
}

func (s *Store) loadExecutorPrepareCandidateRange(ctx context.Context, runID, executorName string, now time.Time,
	rangeKind executorPrepareRange, cursor executorPrepareCursor, limit int,
) ([]executorPrepareCandidate, error) {
	query, args := executorPrepareCandidateQuery(runID, executorName, now, rangeKind, cursor, limit)
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	page := make([]executorPrepareCandidate, 0, limit)
	for rows.Next() {
		var item executorPrepareCandidate
		if err := rows.Scan(&item.runID, &item.nodeID, &item.readyAt, &item.needsJSON,
			&item.requiredCoordinatorID, &item.requiredLocation,
			&item.avoidCoordinatorID, &item.avoidExecutorKind, &item.avoidExecutorID, &item.avoidUntil,
			&item.offerStarted, &item.offerPriorityTarget,
			&item.policyVersion, &item.bodyProtocol,
			&item.supervisorJSON, &item.supervisorRequirementsHash,
			&item.bodyJSON, &item.bodyRequirementsHash); err != nil {
			_ = rows.Close()
			return nil, err
		}
		page = append(page, item)
	}
	err = rows.Err()
	_ = rows.Close()
	return page, err
}

func executorPrepareCandidateQuery(runID, executorName string, now time.Time, rangeKind executorPrepareRange,
	cursor executorPrepareCursor, limit int,
) (string, []any) {
	cursorPredicate := ""
	args := []any{
		executionpolicy.NodeExecutionPolicyVersion, executionpolicy.MaxEncodedPolicyBytes,
		executionpolicy.MaxRuntimeRequirementsJSONBytes, executionpolicy.MaxRuntimeRequirementsJSONBytes,
		executorPrepareNeedsJSONMaxSize,
		runID, runID, executorName, now.Add(-executorOfferLiveness).UnixNano(),
	}
	switch rangeKind {
	case executorPrepareAfterCursor:
		cursorPredicate = `
	   AND (n.ready_at > ? OR (n.ready_at = ? AND
	       (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))))`
		args = append(args, cursor.readyAt, cursor.readyAt, cursor.runID, cursor.runID, cursor.nodeID)
	case executorPrepareThroughCursor:
		cursorPredicate = `
	   AND (n.ready_at < ? OR (n.ready_at = ? AND
	       (n.run_id < ? OR (n.run_id = ? AND n.node_id <= ?))))`
		args = append(args, cursor.readyAt, cursor.readyAt, cursor.runID, cursor.runID, cursor.nodeID)
	}
	args = append(args, limit)
	return `
SELECT n.run_id, n.node_id, n.ready_at, n.needs_labels,
	   n.required_coordinator_id, n.required_executor_location,
	   n.avoid_coordinator_id, n.avoid_executor_kind, n.avoid_executor_id, n.avoid_until,
	   n.offer_started_at, n.offer_priority_target,
	   n.execution_policy_version, n.execution_body_protocol,
	   n.execution_supervisor_requirements_json, n.execution_supervisor_requirements_hash,
	   n.execution_body_requirements_json, n.execution_body_requirements_hash
 FROM nodes n
	WHERE n.ready_at IS NOT NULL AND n.claimed_by IS NULL
	   AND n.outcome = '' AND n.finished_at IS NULL AND n.` + nodeNotDone + `
	   AND n.execution_policy_hash != ''
	   AND n.execution_policy_version = ? AND n.execution_body_protocol > 0
	   AND COALESCE(LENGTH(n.execution_policy_json), 0) > 0
	   AND LENGTH(n.execution_policy_json) <= ?
	   AND COALESCE(LENGTH(n.execution_supervisor_requirements_json), 0) > 0
	   AND LENGTH(n.execution_supervisor_requirements_json) <= ?
	   AND n.execution_supervisor_requirements_hash != ''
	   AND COALESCE(LENGTH(n.execution_body_requirements_json), 0) > 0
	   AND LENGTH(n.execution_body_requirements_json) <= ?
	   AND n.execution_body_requirements_hash != ''
	   AND COALESCE(LENGTH(n.needs_labels), 0) <= ?
	   AND (? = '' OR n.run_id = ?)
	   AND NOT EXISTS (
	       SELECT 1 FROM node_claim_offers o
	        WHERE o.run_id = n.run_id AND o.node_id = n.node_id
	          AND o.executor_name = ? AND o.last_seen_at >= ?)
	` + cursorPredicate + `
	ORDER BY n.ready_at, n.run_id, n.node_id
	LIMIT ?`, args
}

func isExecutorRuntimeRefusal(err error) bool {
	var upgrade *executionpolicy.UpgradeRequiredError
	var protocol *executionpolicy.ProtocolIncompatibleError
	return errors.As(err, &upgrade) || errors.As(err, &protocol)
}

func (s *Store) loadExecutorPrepareCursor(executorName, runID string) executorPrepareCursor {
	s.prepareCursorMu.Lock()
	defer s.prepareCursorMu.Unlock()
	return s.prepareCursors[executorName+"\x00"+runID]
}

func (s *Store) storeExecutorPrepareCursor(executorName, runID string, cursor executorPrepareCursor) {
	s.prepareCursorMu.Lock()
	defer s.prepareCursorMu.Unlock()
	if s.prepareCursors == nil {
		s.prepareCursors = make(map[string]executorPrepareCursor)
	}
	s.prepareCursors[executorName+"\x00"+runID] = cursor
}

func (s *Store) loadExecutorPreparePlans(ctx context.Context, candidates []executorPrepareCandidate) (map[string]executorPreparePlan, error) {
	runSet := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		runSet[candidate.runID] = struct{}{}
	}
	runIDs := make([]string, 0, len(runSet))
	for runID := range runSet {
		runIDs = append(runIDs, runID)
	}
	sort.Strings(runIDs)
	if len(runIDs) == 0 {
		return map[string]executorPreparePlan{}, nil
	}
	args := make([]any, 0, len(runIDs)+1)
	args = append(args, executorPreparePlanJSONMaxSize)
	for _, runID := range runIDs {
		args = append(args, runID)
	}
	rows, err := s.query(ctx, `
SELECT id, pipeline, plan_json FROM runs
 WHERE COALESCE(LENGTH(plan_json), 0) <= ? AND id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	plans := make(map[string]executorPreparePlan, len(runIDs))
	for rows.Next() {
		var runID string
		var plan executorPreparePlan
		if err := rows.Scan(&runID, &plan.pipeline, &plan.raw); err != nil {
			return nil, err
		}
		plans[runID] = plan
	}
	return plans, rows.Err()
}

func (s *Store) loadExecutorPrepareProfiles(ctx context.Context, candidates []executorPrepareCandidate,
	plans map[string]executorPreparePlan,
) (map[string]*PipelineProfile, error) {
	type profileIdentity struct{ pipeline, nodeID string }
	identities := make(map[profileIdentity]struct{}, len(candidates))
	for _, candidate := range candidates {
		if plan, ok := plans[candidate.runID]; ok {
			identities[profileIdentity{pipeline: plan.pipeline, nodeID: candidate.nodeID}] = struct{}{}
		}
	}
	ordered := make([]profileIdentity, 0, len(identities))
	for identity := range identities {
		ordered = append(ordered, identity)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].pipeline != ordered[j].pipeline {
			return ordered[i].pipeline < ordered[j].pipeline
		}
		return ordered[i].nodeID < ordered[j].nodeID
	})
	if len(ordered) == 0 {
		return map[string]*PipelineProfile{}, nil
	}
	var predicate strings.Builder
	args := make([]any, 0, len(ordered)*2)
	for i, identity := range ordered {
		if i != 0 {
			predicate.WriteString(" OR ")
		}
		predicate.WriteString("(pipeline = ? AND node_id = ?)")
		args = append(args, identity.pipeline, identity.nodeID)
	}
	rows, err := s.query(ctx, `SELECT pipeline, node_id, `+profileColumns+`
  FROM pipeline_profiles WHERE `+predicate.String(), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	profiles := make(map[string]*PipelineProfile, len(ordered))
	for rows.Next() {
		var pipeline, nodeID string
		profile, err := scanProfileInto(rows.Scan, &pipeline, &nodeID)
		if err != nil {
			return nil, err
		}
		profiles[executorPrepareProfileKey(pipeline, nodeID)] = profile
	}
	return profiles, rows.Err()
}

func executorPrepareProfileKey(pipeline, nodeID string) string { return pipeline + "\x00" + nodeID }

func hasAlternateEligibleExecutorSnapshot(executors []Executor, usage executorUsageSnapshot, current string,
	summary ExecutorSchedulingSummary, activeAfter time.Time, coordinatorID string,
) bool {
	for _, candidate := range executors {
		if candidate.Name == current {
			continue
		}
		used := usage.ByExecutor[candidate.Name]
		if executorExclusionReason(candidate, summary, used.Active, used.Cores, used.MemoryBytes, activeAfter, coordinatorID) == "" {
			return true
		}
	}
	return false
}

func (s *Store) hasAlternateEligibleExecutor(ctx context.Context, current string, summary ExecutorSchedulingSummary) (bool, error) {
	previews, err := s.PreviewExecutorEligibility(ctx, summary, time.Now().Add(-ExecutorRegistrationActiveWindow))
	if err != nil {
		return false, err
	}
	for _, candidate := range previews {
		if candidate.Name != current && candidate.Eligible {
			return true, nil
		}
	}
	return false, nil
}

// OfferExecutorClaim validates a sealed offer and refuses unattested submissions
// before reservation, recording, or award.
func (s *Store) OfferExecutorClaim(ctx context.Context, claimant ClaimIdentity, offer ExecutorClaimOffer) (ExecutorClaimOfferResult, error) {
	return s.offerExecutorClaimAt(ctx, claimant, offer, time.Now())
}

func (s *Store) offerExecutorClaimAt(ctx context.Context, claimant ClaimIdentity, offer ExecutorClaimOffer, now time.Time) (ExecutorClaimOfferResult, error) {
	if offer.ExecutorName == "" || offer.HolderID == "" || offer.RunID == "" || offer.NodeID == "" ||
		offer.ReservationID == "" || offer.ResourceDigest == "" || offer.Slot < 0 {
		return ExecutorClaimOfferResult{}, errors.New("executor offer requires executor, holder, node, reservation, digest, and slot")
	}
	if err := s.rejectUnattestedExecutorOffer(ctx, claimant, offer); err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	return s.recordExecutorOfferAt(ctx, claimant, offer, now)
}

func (s *Store) recordExecutorOfferAt(ctx context.Context, claimant ClaimIdentity, offer ExecutorClaimOffer, now time.Time) (ExecutorClaimOfferResult, error) {
	offer.Lease = clampNodeLease(offer.Lease)
	if err := s.ValidateExecutorClaimReservation(ctx, claimant, offer.RunID, offer.NodeID,
		offer.ExecutorName, offer.ReservationID, offer.Slot, offer.ResourceDigest); err == nil {
		n, err := s.GetNode(ctx, offer.RunID, offer.NodeID)
		if err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		if n.ClaimedBy == offer.HolderID {
			return ExecutorClaimOfferResult{Node: n}, nil
		}
	}
	summary, err := s.SchedulingSummary(ctx, offer.RunID, offer.NodeID)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if summary.ResourceDigest != offer.ResourceDigest {
		return ExecutorClaimOfferResult{}, ErrLockHeld
	}
	membership, err := s.resolveExecutorMembership(ctx, claimant, offer.ExecutorName, summary, now)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if !membership.Eligible || offer.Slot >= membership.MaxConcurrent {
		return ExecutorClaimOfferResult{}, ErrNotFound
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if n, ok, err := claimedExecutorOffer(ctx, tx, claimant, offer, now); err != nil {
		return ExecutorClaimOfferResult{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		return ExecutorClaimOfferResult{Node: n}, nil
	}

	n := &nodeRecord{}
	err = scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL
   AND claimed_by IS NULL AND `+nodeNotDone+tx.forUpdate(), offer.RunID, offer.NodeID), n)
	if errors.Is(err, ErrNotFound) {
		_, _ = tx.ExecContext(ctx, `DELETE FROM node_claim_offers
 WHERE claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?`, claimant.TokenPrefix, claimant.Principal, offer.HolderID)
		if err := tx.Commit(); err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		return ExecutorClaimOfferResult{}, nil
	}
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	summary, err = s.schedulingSummaryTx(ctx, tx, offer.RunID, offer.NodeID)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if summary.ResourceDigest != offer.ResourceDigest {
		return ExecutorClaimOfferResult{}, ErrLockHeld
	}
	membership, err = s.resolveExecutorMembershipTx(ctx, tx, claimant, offer.ExecutorName, summary, now)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if !membership.Eligible || offer.Slot >= membership.MaxConcurrent {
		return ExecutorClaimOfferResult{}, ErrNotFound
	}
	if err := s.expireConflictingExecutorOffersTx(ctx, tx, offer, now); err != nil {
		return ExecutorClaimOfferResult{}, err
	}

	var conflicts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_claim_offers
 WHERE (reservation_id = ? OR (executor_name = ? AND slot = ?)
        OR (executor_name = ? AND run_id = ? AND node_id = ?))
   AND NOT (claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?)`,
		offer.ReservationID, offer.ExecutorName, offer.Slot,
		offer.ExecutorName, offer.RunID, offer.NodeID,
		claimant.TokenPrefix, claimant.Principal, offer.HolderID).Scan(&conflicts); err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if conflicts != 0 {
		return ExecutorClaimOfferResult{}, ErrLockHeld
	}

	offeredAt := now.UnixNano()
	var priorRunID, priorNodeID, priorReservationID, priorDigest string
	var priorSlot int
	var priorOfferedAt int64
	priorErr := tx.QueryRowContext(ctx, `SELECT run_id, node_id, reservation_id, resource_digest, slot, offered_at
  FROM node_claim_offers
 WHERE claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?`+tx.forUpdate(),
		claimant.TokenPrefix, claimant.Principal, offer.HolderID).Scan(
		&priorRunID, &priorNodeID, &priorReservationID, &priorDigest, &priorSlot, &priorOfferedAt)
	if priorErr == nil && priorRunID == offer.RunID && priorNodeID == offer.NodeID &&
		priorReservationID == offer.ReservationID && priorDigest == offer.ResourceDigest && priorSlot == offer.Slot {
		offeredAt = priorOfferedAt
	} else if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return ExecutorClaimOfferResult{}, priorErr
	}
	newOffer := priorErr != nil || priorRunID != offer.RunID || priorNodeID != offer.NodeID ||
		priorReservationID != offer.ReservationID || priorDigest != offer.ResourceDigest || priorSlot != offer.Slot
	if newOffer {
		count, err := nodeExecutorOfferCountTx(ctx, tx, offer.RunID, offer.NodeID)
		if err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		if count >= MaxEnrolledExecutors {
			return ExecutorClaimOfferResult{}, errExecutorOfferLimit
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_claim_offers
       (claim_token_prefix, claim_principal, holder_id, run_id, node_id,
        executor_name, membership_id, worker_id, executor_kind, reservation_id,
        resource_digest, slot, base_priority, effective_priority, offered_at, last_seen_at, lease_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (claim_token_prefix, claim_principal, holder_id) DO UPDATE SET
       run_id = excluded.run_id, node_id = excluded.node_id,
       executor_name = excluded.executor_name, membership_id = excluded.membership_id,
       worker_id = excluded.worker_id, executor_kind = excluded.executor_kind,
       reservation_id = excluded.reservation_id, resource_digest = excluded.resource_digest,
       slot = excluded.slot, base_priority = excluded.base_priority,
       effective_priority = excluded.effective_priority, offered_at = excluded.offered_at,
       last_seen_at = excluded.last_seen_at, lease_ns = excluded.lease_ns`,
		claimant.TokenPrefix, claimant.Principal, offer.HolderID, offer.RunID, offer.NodeID,
		offer.ExecutorName, membership.MembershipID, membership.WorkerID, membership.Kind, offer.ReservationID,
		offer.ResourceDigest, offer.Slot, membership.RegisteredBasePriority, membership.EffectivePriority,
		offeredAt, now.UnixNano(), int64(offer.Lease)); err != nil {
		if isUniqueViolation(err) {
			return ExecutorClaimOfferResult{}, ErrLockHeld
		}
		return ExecutorClaimOfferResult{}, err
	}
	if newOffer {
		executor, err := s.getExecutorTx(ctx, tx, offer.ExecutorName)
		if err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		if _, err := appendEventTx(ctx, tx, offer.RunID, offer.NodeID, "executor_offer_received",
			executorOfferEventFor(summary, membership, executor.Location, n.OfferPriorityTarget, offer.Slot), now); err != nil {
			return ExecutorClaimOfferResult{}, err
		}
	}

	deadlineReached := n.OfferStartedAt != nil && !now.Before(n.OfferStartedAt.Add(nodeClaimOfferWindow))
	due := membership.EffectivePriority == 100 || membership.EffectivePriority >= n.OfferPriorityTarget || deadlineReached
	if !due {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		return ExecutorClaimOfferResult{Pending: true}, nil
	}
	awardReason := "priority_target"
	if deadlineReached {
		awardReason = "deadline"
	}
	winner, err := s.awardBestExecutorOffer(ctx, tx, offer.RunID, offer.NodeID, now, awardReason)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if winner != nil && winner.ExecutorName == offer.ExecutorName && winner.ReservationID == offer.ReservationID && winner.HolderID == offer.HolderID {
		return ExecutorClaimOfferResult{Node: winner.Node}, nil
	}
	return ExecutorClaimOfferResult{}, nil
}

func (s *Store) rejectUnattestedExecutorOffer(ctx context.Context, claimant ClaimIdentity, offer ExecutorClaimOffer) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	executor, err := s.getExecutorTx(ctx, tx, offer.ExecutorName)
	if err != nil {
		return err
	}
	if claimant.TokenPrefix == "" || executor.TokenPrefix != claimant.TokenPrefix || executor.Principal != claimant.Principal {
		return ErrExecutorCredentialMismatch
	}
	record := &nodeRecord{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL
	AND claimed_by IS NULL AND `+nodeNotDone+tx.forUpdate(), offer.RunID, offer.NodeID), record); err != nil {
		return err
	}
	policy, sealed, err := policyForNodeExecution(record)
	if err != nil {
		return err
	}
	if !sealed {
		return ErrNotFound
	}
	runtimeReport, err := executorRuntimeReportTx(ctx, tx, offer.ExecutorName)
	if err != nil {
		return err
	}
	if err := executionpolicy.CheckRuntimeCompatibility(policy, runtimeReport); err != nil {
		return err
	}
	want, err := claimBindingForNodeExecution(record)
	if err != nil {
		return err
	}
	got, ok := executionpolicy.OfferBindingFromContext(ctx)
	if !ok || got != want {
		return ErrLockHeld
	}
	return &executionpolicy.BodyAttestationRequiredError{RunID: offer.RunID, NodeID: offer.NodeID}
}

type executorOfferWinner struct {
	ExecutorName, ExecutorID, MembershipID, ExecutorKind, HolderID, ReservationID, ResourceDigest string
	ExecutorLocation                                                                              string
	Claimant                                                                                      ClaimIdentity
	Slot, BasePriority, EffectivePriority                                                         int
	OfferedAt, LastSeenAt                                                                         int64
	Lease                                                                                         time.Duration
	Node                                                                                          *Node
}

func (s *Store) awardBestExecutorOffer(ctx context.Context, tx *storeTx, runID, nodeID string, now time.Time, awardReason string) (*executorOfferWinner, error) {
	if err := ensureExecutorCardinalityTx(ctx, tx); err != nil {
		return nil, err
	}
	if err := ensureNodeExecutorOfferCardinalityTx(ctx, tx, runID, nodeID); err != nil {
		return nil, err
	}
	var lockedRun string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE id = ?`+tx.forUpdate(), runID).Scan(&lockedRun); err != nil {
		return nil, err
	}
	summary, err := s.schedulingSummaryTx(ctx, tx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	var priorityTarget int
	if err := tx.QueryRowContext(ctx, `SELECT offer_priority_target FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID).Scan(&priorityTarget); err != nil {
		return nil, err
	}
	if err := s.expireNodeExecutorOffersTx(ctx, tx, summary, priorityTarget, now); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT executor_name, membership_id, claim_principal, claim_token_prefix, holder_id,
	   reservation_id, resource_digest, slot, base_priority, effective_priority,
	   executor_kind, offered_at, last_seen_at, lease_ns
  FROM node_claim_offers
 WHERE run_id = ? AND node_id = ? AND last_seen_at >= ?
 LIMIT ?`+tx.forUpdate(),
		runID, nodeID, now.Add(-executorOfferLiveness).UnixNano(), MaxEnrolledExecutors+1)
	if err != nil {
		return nil, err
	}
	var candidates []executorOfferWinner
	for rows.Next() {
		var item executorOfferWinner
		var leaseNS int64
		if err := rows.Scan(&item.ExecutorName, &item.MembershipID, &item.Claimant.Principal, &item.Claimant.TokenPrefix,
			&item.HolderID, &item.ReservationID, &item.ResourceDigest, &item.Slot, &item.BasePriority,
			&item.EffectivePriority, &item.ExecutorKind, &item.OfferedAt, &item.LastSeenAt, &leaseNS); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.Lease = clampNodeLease(time.Duration(leaseNS))
		candidates = append(candidates, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates) > MaxEnrolledExecutors {
		return nil, ErrExecutorEnrollmentLimit
	}
	names := make([]string, 0, len(candidates))
	for i := range candidates {
		names = append(names, candidates[i].ExecutorName)
	}
	if err := lockExistingExecutorRowsCanonicalTx(ctx, tx, names...); err != nil {
		return nil, err
	}
	now = time.Now()
	// safety: allocation and live-lease extension fence the same executor rows, so one
	// occupancy scan stays valid until this transaction records its winner.
	executors, err := loadExecutorsByNameTx(ctx, tx, names)
	if err != nil {
		return nil, err
	}
	usage, err := loadExecutorUsageTx(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	authorityID, err := controllerAuthorityIDTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	live := candidates[:0]
	for i := range candidates {
		item := &candidates[i]
		executor, found := executors[item.ExecutorName]
		membership, err := executorMembershipFromSnapshot(
			executor, item.Claimant, summary, usage.ByExecutor[item.ExecutorName], authorityID, coordinatorID,
			now.Add(-ExecutorRegistrationActiveWindow),
		)
		if !found {
			err = ErrNotFound
		}
		if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrExecutorCredentialMismatch) {
			return nil, err
		}
		_, slotBusy := usage.ByExecutor[item.ExecutorName].Slots[item.Slot]
		_, reservationBusy := usage.Reservations[item.ReservationID]
		offerExpired := item.LastSeenAt < now.Add(-executorOfferLiveness).UnixNano()
		if err != nil || offerExpired || !membership.Eligible || item.Slot >= membership.MaxConcurrent ||
			item.ResourceDigest != summary.ResourceDigest || slotBusy || reservationBusy {
			reason := "ineligible"
			if offerExpired {
				reason = "liveness_expired"
			} else if errors.Is(err, ErrNotFound) {
				reason = "enrollment_missing"
			} else if errors.Is(err, ErrExecutorCredentialMismatch) {
				reason = "credential_changed"
			} else if item.ResourceDigest != summary.ResourceDigest {
				reason = "requirements_changed"
			} else if err == nil && item.Slot >= membership.MaxConcurrent {
				reason = "slot_limit_changed"
			} else if slotBusy {
				reason = "slot_in_use"
			} else if reservationBusy {
				reason = "reservation_in_use"
			}
			event := executorOfferEventFor(summary, membership, "", priorityTarget, item.Slot)
			event.ExecutorName = item.ExecutorName
			event.ExecutorKind = item.ExecutorKind
			event.EffectivePriority = item.EffectivePriority
			event.BasePriority = item.BasePriority
			event.Reason = reason
			eventType := "executor_offer_declined"
			if offerExpired {
				eventType = "executor_offer_expired"
				event.Outcome = "expired"
			}
			if found {
				event.ExecutorKind = executor.Kind
				event.ExecutorLocation = executor.Location
			}
			if _, err := appendEventTx(ctx, tx, runID, nodeID, eventType, event, now); err != nil {
				return nil, err
			}
			if _, deleteErr := tx.ExecContext(ctx, `DELETE FROM node_claim_offers
 WHERE claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?`,
				item.Claimant.TokenPrefix, item.Claimant.Principal, item.HolderID); deleteErr != nil {
				return nil, deleteErr
			}
			continue
		}
		item.MembershipID = membership.MembershipID
		item.ExecutorID = executor.id
		item.ExecutorKind = membership.Kind
		item.ExecutorLocation = executor.Location
		item.BasePriority = membership.RegisteredBasePriority
		item.EffectivePriority = membership.EffectivePriority
		live = append(live, *item)
	}
	candidates = live
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.EffectivePriority != right.EffectivePriority {
			return left.EffectivePriority > right.EffectivePriority
		}
		if left.OfferedAt != right.OfferedAt {
			return left.OfferedAt < right.OfferedAt
		}
		if left.ExecutorName != right.ExecutorName {
			return left.ExecutorName < right.ExecutorName
		}
		if left.Slot != right.Slot {
			return left.Slot < right.Slot
		}
		return left.HolderID < right.HolderID
	})
	if len(candidates) == 0 {
		return nil, ErrNotFound
	}
	item := &candidates[0]
	n := &nodeRecord{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL
   AND claimed_by IS NULL AND `+nodeNotDone, runID, nodeID), n); err != nil {
		return nil, err
	}
	expires := now.Add(item.Lease)
	res, err := tx.ExecContext(ctx, `UPDATE nodes SET
       claimed_by = ?, claim_principal = ?, claim_token_prefix = ?,
       claim_executor = ?, claim_cores = ?, claim_memory_bytes = ?,
       claim_reservation = ?, claim_slot = ?, lease_expires_at = ?,
       claim_base_priority = ?, claim_priority = ?, claim_worker_id = ?, claim_executor_kind = ?,
	   claim_reservation_id = ?, coordinator_id = ?, claim_membership_id = ?,
	   executor_kind = ?, executor_id = ?, executor_location = ?, reservation_id = ?,
	   claim_generation = claim_generation + 1,
	   required_coordinator_id = CASE WHEN required_coordinator_id = '' THEN ? ELSE required_coordinator_id END,
	   required_executor_location = CASE WHEN required_executor_location = '' THEN ? ELSE required_executor_location END
 WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL
	   AND claimed_by IS NULL AND `+nodeNotDone+`
	   AND (required_coordinator_id = '' OR required_coordinator_id = ?)
	   AND (required_executor_location = '' OR (required_executor_location != 'unknown' AND required_executor_location = ?))`,
		item.HolderID, item.Claimant.Principal, item.Claimant.TokenPrefix,
		item.ExecutorName, summary.Resources.Cores, summary.Resources.MemoryBytes,
		item.ReservationID, item.Slot, expires.UnixNano(),
		item.BasePriority, item.EffectivePriority, item.ExecutorName, item.ExecutorKind,
		item.ReservationID, coordinatorID, item.MembershipID, item.ExecutorKind, item.ExecutorID,
		item.ExecutorLocation, item.ReservationID, coordinatorID, item.ExecutorLocation,
		runID, nodeID, coordinatorID, item.ExecutorLocation)
	if err != nil {
		return nil, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, ErrLockHeld
	}
	n.ClaimedBy = item.HolderID
	n.LeaseExpiresAt = &expires
	n.ClaimBasePriority = item.BasePriority
	n.ClaimPriority = item.EffectivePriority
	n.ClaimWorkerID = item.ExecutorName
	n.ClaimExecutorKind = item.ExecutorKind
	n.ClaimReservationID = item.ReservationID
	n.CoordinatorID = coordinatorID
	n.ClaimMembershipID = item.MembershipID
	n.ExecutorKind = item.ExecutorKind
	n.ExecutorID = item.ExecutorID
	n.ExecutorLocation = item.ExecutorLocation
	n.ReservationID = item.ReservationID
	n.ClaimGeneration++
	if n.RequiredCoordinatorID == "" {
		n.RequiredCoordinatorID = coordinatorID
	}
	if n.RequiredExecutorLocation == "" {
		n.RequiredExecutorLocation = item.ExecutorLocation
	}
	item.Node = &n.Node
	selected := executionAttributionEventFields(item.ExecutorKind, item.ExecutorName, item.ExecutorLocation)
	if n.AvoidUntil != nil && n.AvoidUntil.After(now) {
		selected["avoid_until"] = n.AvoidUntil
		if n.AvoidExecutorID == item.ExecutorID {
			selected["avoided_executor_kind"] = item.ExecutorKind
			selected["avoided_executor_name"] = item.ExecutorName
		}
	}
	if _, err := appendEventTx(ctx, tx, runID, nodeID, "executor_selected", selected, now); err != nil {
		return nil, err
	}
	winnerEvent := executorOfferEvent{
		ExecutorName: item.ExecutorName,
		ExecutorKind: item.ExecutorKind, ExecutorLocation: item.ExecutorLocation,
		BasePriority: item.BasePriority, EffectivePriority: item.EffectivePriority, PriorityTarget: priorityTarget,
		HardCapabilities: summary.HardCapabilities, PreferredCapabilities: summary.PreferredCapabilities,
		Resources: summary.Resources, Slots: summary.Slots, Reason: awardReason, Outcome: "awarded",
		Slot: offerSlot(item.Slot),
	}
	if _, err := appendEventTx(ctx, tx, runID, nodeID, "executor_offer_awarded", winnerEvent, now); err != nil {
		return nil, err
	}
	for loserIndex := 1; loserIndex < len(candidates); loserIndex++ {
		loser := &candidates[loserIndex]
		loserEvent := executorOfferEvent{
			ExecutorName: loser.ExecutorName,
			ExecutorKind: loser.ExecutorKind, ExecutorLocation: loser.ExecutorLocation,
			BasePriority: loser.BasePriority, EffectivePriority: loser.EffectivePriority, PriorityTarget: priorityTarget,
			HardCapabilities: summary.HardCapabilities, PreferredCapabilities: summary.PreferredCapabilities,
			Resources: summary.Resources, Slots: summary.Slots, Reason: "lower_ranked", Outcome: "declined",
			Slot: offerSlot(loser.Slot),
		}
		if _, err := appendEventTx(ctx, tx, runID, nodeID, "executor_offer_declined", loserEvent, now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, nodeID); err != nil {
		return nil, err
	}
	return item, nil
}

func ensureNodeExecutorOfferCardinalityTx(ctx context.Context, tx *storeTx, runID, nodeID string) error {
	count, err := nodeExecutorOfferCountTx(ctx, tx, runID, nodeID)
	if err != nil {
		return err
	}
	if count > MaxEnrolledExecutors {
		return errExecutorOfferLimit
	}
	return nil
}

func nodeExecutorOfferCountTx(ctx context.Context, tx *storeTx, runID, nodeID string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT holder_id FROM node_claim_offers
 WHERE run_id = ? AND node_id = ? LIMIT ?`, runID, nodeID, MaxEnrolledExecutors+1)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) expireNodeExecutorOffersTx(ctx context.Context, tx *storeTx, summary ExecutorSchedulingSummary, priorityTarget int, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
SELECT o.executor_name, o.executor_kind, o.base_priority, o.effective_priority, o.slot, COALESCE(e.location, '')
  FROM node_claim_offers o LEFT JOIN executors e ON e.name = o.executor_name
 WHERE o.run_id = ? AND o.node_id = ? AND o.last_seen_at < ?`,
		summary.RunID, summary.NodeID, now.Add(-executorOfferLiveness).UnixNano())
	if err != nil {
		return err
	}
	var expired []executorOfferEvent
	for rows.Next() {
		var event executorOfferEvent
		var slot int
		if err := rows.Scan(&event.ExecutorName, &event.ExecutorKind, &event.BasePriority, &event.EffectivePriority, &slot, &event.ExecutorLocation); err != nil {
			_ = rows.Close()
			return err
		}
		event.Slot = offerSlot(slot)
		event.HardCapabilities = summary.HardCapabilities
		event.PreferredCapabilities = summary.PreferredCapabilities
		event.Resources = summary.Resources
		event.Slots = summary.Slots
		event.PriorityTarget = priorityTarget
		event.Reason = "liveness_expired"
		event.Outcome = "expired"
		expired = append(expired, event)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, event := range expired {
		if _, err := appendEventTx(ctx, tx, summary.RunID, summary.NodeID, "executor_offer_expired", event, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM node_claim_offers
 WHERE run_id = ? AND node_id = ? AND last_seen_at < ?`,
		summary.RunID, summary.NodeID, now.Add(-executorOfferLiveness).UnixNano())
	return err
}

func (s *Store) expireConflictingExecutorOffersTx(ctx context.Context, tx *storeTx, offer ExecutorClaimOffer, now time.Time) error {
	cutoff := now.Add(-executorOfferLiveness).UnixNano()
	rows, err := tx.QueryContext(ctx, `
SELECT o.run_id, o.node_id, o.executor_name, o.executor_kind, o.base_priority,
       o.effective_priority, o.slot, COALESCE(e.location, '')
  FROM node_claim_offers o LEFT JOIN executors e ON e.name = o.executor_name
 WHERE o.last_seen_at < ? AND (o.reservation_id = ? OR (o.executor_name = ? AND o.slot = ?)
        OR (o.executor_name = ? AND o.run_id = ? AND o.node_id = ?))`,
		cutoff, offer.ReservationID, offer.ExecutorName, offer.Slot,
		offer.ExecutorName, offer.RunID, offer.NodeID)
	if err != nil {
		return err
	}
	type expiredOffer struct {
		runID, nodeID string
		event         executorOfferEvent
	}
	var expired []expiredOffer
	for rows.Next() {
		var item expiredOffer
		var slot int
		if err := rows.Scan(&item.runID, &item.nodeID, &item.event.ExecutorName, &item.event.ExecutorKind,
			&item.event.BasePriority, &item.event.EffectivePriority, &slot, &item.event.ExecutorLocation); err != nil {
			_ = rows.Close()
			return err
		}
		item.event.Slot = offerSlot(slot)
		expired = append(expired, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, item := range expired {
		summary, err := s.schedulingSummaryTx(ctx, tx, item.runID, item.nodeID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil {
			item.event.HardCapabilities = summary.HardCapabilities
			item.event.PreferredCapabilities = summary.PreferredCapabilities
			item.event.Resources = summary.Resources
			item.event.Slots = summary.Slots
		}
		if err := tx.QueryRowContext(ctx, `SELECT offer_priority_target FROM nodes WHERE run_id = ? AND node_id = ?`, item.runID, item.nodeID).Scan(&item.event.PriorityTarget); err != nil {
			return err
		}
		item.event.Reason = "liveness_expired"
		item.event.Outcome = "expired"
		if _, err := appendEventTx(ctx, tx, item.runID, item.nodeID, "executor_offer_expired", item.event, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM node_claim_offers
 WHERE last_seen_at < ? AND (reservation_id = ? OR (executor_name = ? AND slot = ?)
        OR (executor_name = ? AND run_id = ? AND node_id = ?))`,
		cutoff, offer.ReservationID, offer.ExecutorName, offer.Slot,
		offer.ExecutorName, offer.RunID, offer.NodeID)
	return err
}

func claimedExecutorOffer(ctx context.Context, tx *storeTx, claimant ClaimIdentity, offer ExecutorClaimOffer, now time.Time) (*Node, bool, error) {
	var holderID, principal, tokenPrefix, executorName, reservationID sql.NullString
	var slot int
	var expires int64
	err := tx.QueryRowContext(ctx, `SELECT claimed_by, claim_principal, claim_token_prefix,
       claim_executor, claim_reservation, claim_slot, COALESCE(lease_expires_at, 0)
  FROM nodes WHERE run_id = ? AND node_id = ?`, offer.RunID, offer.NodeID).Scan(
		&holderID, &principal, &tokenPrefix, &executorName, &reservationID, &slot, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if holderID.String != offer.HolderID || principal.String != claimant.Principal || tokenPrefix.String != claimant.TokenPrefix ||
		executorName.String != offer.ExecutorName || reservationID.String != offer.ReservationID || slot != offer.Slot || expires < now.UnixNano() {
		return nil, false, nil
	}
	n := &nodeRecord{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes WHERE run_id = ? AND node_id = ?`, offer.RunID, offer.NodeID), n); err != nil {
		return nil, false, err
	}
	return &n.Node, true, nil
}

// FinalizeExecutorClaimRound awards the best live offer after the deadline or
// atomically transfers the still-unclaimed node to coordinator fallback.
func (s *Store) FinalizeExecutorClaimRound(ctx context.Context, runID, nodeID string) (ExecutorClaimRoundResult, error) {
	return s.finalizeExecutorClaimRoundAt(ctx, runID, nodeID, time.Now())
}

func (s *Store) finalizeExecutorClaimRoundAt(ctx context.Context, runID, nodeID string, now time.Time) (ExecutorClaimRoundResult, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	var readyAt, opened sql.NullInt64
	var claimedBy sql.NullString
	var status, requiredCoordinator, requiredLocation string
	err = tx.QueryRowContext(ctx, `SELECT ready_at, claimed_by, status, offer_started_at,
       required_coordinator_id, required_executor_location FROM nodes
	 WHERE run_id = ? AND node_id = ?`+tx.forUpdate(), runID, nodeID).Scan(
		&readyAt, &claimedBy, &status, &opened, &requiredCoordinator, &requiredLocation)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutorClaimRoundResult{}, ErrNotFound
	}
	if err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	if !readyAt.Valid || claimedBy.Valid || status == nodeStatusDone {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		return ExecutorClaimRoundResult{}, nil
	}
	if opened.Valid && now.Before(time.Unix(0, opened.Int64).Add(nodeClaimOfferWindow)) {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		return ExecutorClaimRoundResult{Pending: true}, nil
	}
	if winner, err := s.awardBestExecutorOffer(ctx, tx, runID, nodeID, now, "deadline"); err == nil && winner != nil {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		return ExecutorClaimRoundResult{}, nil
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return ExecutorClaimRoundResult{}, err
	}
	if requiredCoordinator != "" || requiredLocation != "" {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		return ExecutorClaimRoundResult{Pending: true}, nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE nodes SET ready_at = NULL, offer_started_at = NULL
 WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL AND `+nodeNotDone, runID, nodeID)
	if err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, nodeID); err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	if changed == 1 {
		summary, err := s.schedulingSummaryTx(ctx, tx, runID, nodeID)
		if err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		var target int
		if err := tx.QueryRowContext(ctx, `SELECT offer_priority_target FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID).Scan(&target); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		if _, err := appendEventTx(ctx, tx, runID, nodeID, "executor_offer_round_empty", executorOfferEvent{
			PriorityTarget: target, HardCapabilities: summary.HardCapabilities,
			PreferredCapabilities: summary.PreferredCapabilities, Resources: summary.Resources,
			Slots: summary.Slots, Reason: "no_live_offer", Outcome: "coordinator_fallback",
		}, now); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	return ExecutorClaimRoundResult{Revoked: changed == 1}, nil
}
