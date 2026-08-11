package livechainacceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

var ErrSessionConflict = errors.New("acceptance session compare-and-swap conflict")

type SessionPhase string

const (
	SessionStarted              SessionPhase = "started"
	SessionAPrepared            SessionPhase = "a_prepared"
	SessionADeployIntent        SessionPhase = "a_deploy_intent"
	SessionADeployed            SessionPhase = "a_deployed"
	SessionAHealthy             SessionPhase = "a_healthy"
	SessionANotifyIntent        SessionPhase = "a_notify_intent"
	SessionAAccepted            SessionPhase = "a_accepted"
	SessionBPrepared            SessionPhase = "b_prepared"
	SessionBDeployIntent        SessionPhase = "b_deploy_intent"
	SessionBDeployed            SessionPhase = "b_deployed"
	SessionBHealthy             SessionPhase = "b_healthy"
	SessionBNotifyIntent        SessionPhase = "b_notify_intent"
	SessionBAccepted            SessionPhase = "b_accepted"
	SessionFaultIntent          SessionPhase = "fault_intent"
	SessionFaultInjected        SessionPhase = "fault_injected"
	SessionFailureObserved      SessionPhase = "failure_observed"
	SessionFailureNotifyIntent  SessionPhase = "failure_notify_intent"
	SessionFailureNotified      SessionPhase = "failure_notified"
	SessionCleanupIntent        SessionPhase = "cleanup_intent"
	SessionCleaned              SessionPhase = "cleaned"
	SessionRollbackIntent       SessionPhase = "rollback_intent"
	SessionRolledBack           SessionPhase = "rolled_back"
	SessionRollbackHealthy      SessionPhase = "rollback_healthy"
	SessionRollbackNotifyIntent SessionPhase = "rollback_notify_intent"
	SessionComplete             SessionPhase = "complete"
	SessionFailed               SessionPhase = "failed"
	SessionCleanupFailed        SessionPhase = "cleanup_failed"
)

type SessionSeed struct {
	ID     string
	Events [2]LandEvent
}

type Session struct {
	ID                  string
	SeedDigest          string
	Version             uint64
	PreviousStateDigest string
	StateSeal           StateSeal
	Phase               SessionPhase
	Events              [2]LandEvent
	Proof               Proof
	TerminalError       string
	PhaseDeadline       time.Time
}

type StateSeal struct {
	Domain        string
	SchemaVersion string
	KeyID         string
	Algorithm     string
	SignedAt      time.Time
	Digest        string
	Signature     string
}

type proofField uint8

const (
	proofEvents proofField = iota
	proofProductionA
	proofProductionB
	proofArtifactA
	proofArtifactB
	proofDeploymentA
	proofDeploymentB
	proofHealthA
	proofHealthB
	proofNotificationA
	proofNotificationB
	proofFault
	proofFailure
	proofFailureNotice
	proofCleanup
	proofRollback
	proofRollbackHealth
	proofRollbackNotice
)

type transitionEdge struct {
	from SessionPhase
	to   SessionPhase
}

type transitionRule struct {
	proofDelta        []proofField
	optionalProof     []proofField
	maySetTerminalErr bool
}

var sessionTransitionRules = map[transitionEdge]transitionRule{
	{SessionStarted, SessionAPrepared}:                    {proofDelta: []proofField{proofEvents, proofProductionA, proofArtifactA}},
	{SessionStarted, SessionFailed}:                       {maySetTerminalErr: true},
	{SessionAPrepared, SessionADeployIntent}:              {},
	{SessionAPrepared, SessionFailed}:                     {maySetTerminalErr: true},
	{SessionADeployIntent, SessionADeployed}:              {proofDelta: []proofField{proofDeploymentA}},
	{SessionADeployIntent, SessionFailed}:                 {maySetTerminalErr: true},
	{SessionADeployed, SessionAHealthy}:                   {proofDelta: []proofField{proofHealthA}},
	{SessionADeployed, SessionFailed}:                     {maySetTerminalErr: true},
	{SessionAHealthy, SessionANotifyIntent}:               {},
	{SessionAHealthy, SessionFailed}:                      {maySetTerminalErr: true},
	{SessionANotifyIntent, SessionAAccepted}:              {proofDelta: []proofField{proofNotificationA}},
	{SessionANotifyIntent, SessionFailed}:                 {maySetTerminalErr: true},
	{SessionAAccepted, SessionBPrepared}:                  {proofDelta: []proofField{proofProductionB, proofArtifactB}},
	{SessionAAccepted, SessionFailed}:                     {maySetTerminalErr: true},
	{SessionBPrepared, SessionBDeployIntent}:              {},
	{SessionBPrepared, SessionFailed}:                     {maySetTerminalErr: true},
	{SessionBDeployIntent, SessionBDeployed}:              {proofDelta: []proofField{proofDeploymentB}},
	{SessionBDeployIntent, SessionFailed}:                 {maySetTerminalErr: true},
	{SessionBDeployed, SessionBHealthy}:                   {proofDelta: []proofField{proofHealthB}},
	{SessionBDeployed, SessionFailed}:                     {maySetTerminalErr: true},
	{SessionBHealthy, SessionBNotifyIntent}:               {},
	{SessionBHealthy, SessionFailed}:                      {maySetTerminalErr: true},
	{SessionBNotifyIntent, SessionBAccepted}:              {proofDelta: []proofField{proofNotificationB}},
	{SessionBNotifyIntent, SessionFailed}:                 {maySetTerminalErr: true},
	{SessionBAccepted, SessionFaultIntent}:                {},
	{SessionBAccepted, SessionFailed}:                     {maySetTerminalErr: true},
	{SessionFaultIntent, SessionFaultInjected}:            {proofDelta: []proofField{proofFault}},
	{SessionFaultIntent, SessionCleanupIntent}:            {optionalProof: []proofField{proofFault}, maySetTerminalErr: true},
	{SessionFaultIntent, SessionFailed}:                   {maySetTerminalErr: true},
	{SessionFaultInjected, SessionFailureObserved}:        {proofDelta: []proofField{proofFailure}},
	{SessionFaultInjected, SessionCleanupIntent}:          {maySetTerminalErr: true},
	{SessionFailureObserved, SessionFailureNotifyIntent}:  {},
	{SessionFailureObserved, SessionCleanupIntent}:        {maySetTerminalErr: true},
	{SessionFailureNotifyIntent, SessionFailureNotified}:  {proofDelta: []proofField{proofFailureNotice}},
	{SessionFailureNotifyIntent, SessionCleanupIntent}:    {maySetTerminalErr: true},
	{SessionFailureNotified, SessionCleanupIntent}:        {},
	{SessionCleanupIntent, SessionCleaned}:                {proofDelta: []proofField{proofCleanup}},
	{SessionCleanupIntent, SessionFailed}:                 {proofDelta: []proofField{proofCleanup}},
	{SessionCleanupIntent, SessionCleanupFailed}:          {maySetTerminalErr: true},
	{SessionCleanupFailed, SessionCleanupIntent}:          {},
	{SessionCleaned, SessionRollbackIntent}:               {},
	{SessionCleaned, SessionFailed}:                       {maySetTerminalErr: true},
	{SessionRollbackIntent, SessionRolledBack}:            {proofDelta: []proofField{proofRollback}},
	{SessionRollbackIntent, SessionFailed}:                {maySetTerminalErr: true},
	{SessionRolledBack, SessionRollbackHealthy}:           {proofDelta: []proofField{proofRollbackHealth}},
	{SessionRolledBack, SessionFailed}:                    {maySetTerminalErr: true},
	{SessionRollbackHealthy, SessionRollbackNotifyIntent}: {},
	{SessionRollbackHealthy, SessionFailed}:               {maySetTerminalErr: true},
	{SessionRollbackNotifyIntent, SessionComplete}:        {proofDelta: []proofField{proofRollbackNotice}},
	{SessionRollbackNotifyIntent, SessionFailed}:          {maySetTerminalErr: true},
}

type SessionStore interface {
	// LoadOrCreate returns the append-only current tail. It must reject a
	// lower-version replay even when that historical state has a valid seal.
	// It may invoke the initial sealer only inside a winning create-if-absent
	// transaction; loading an existing tail must not mint another genesis.
	LoadOrCreate(context.Context, SessionSeed, InitialSessionFactory, SessionVerifier) (Session, error)
	// CompareAndSwap appends exactly one successor after matching the current
	// tail's ID, seed, version, and authenticated state digest.
	CompareAndSwap(context.Context, string, uint64, string, string, Session, SessionVerifier) error
}

type InitialSessionFactory func(context.Context) (Session, error)

type SessionVerifier interface {
	Verify(context.Context, Session) error
}

type SessionSigner interface {
	// InitialFactory owns canonicalization of the exact version-one genesis.
	// The returned factory may be invoked only by
	// the create-if-absent authority and replays one identical sealed result.
	InitialFactory(SessionSeed) InitialSessionFactory
	// SealSuccessor verifies current and enforces immutable identity,
	// predecessor linkage, version+1, legal transition, append-only proof, and
	// cleanup irreversibility before returning a signed canonical successor.
	SealSuccessor(context.Context, Session, Session) (Session, error)
}

type SessionAuthenticator interface {
	SessionSigner
	SessionVerifier
}

type EffectKind string

const (
	EffectDeployA         EffectKind = "deploy_a"
	EffectNotifyAcceptedA EffectKind = "notify_accepted_a"
	EffectDeployB         EffectKind = "deploy_b"
	EffectNotifyAcceptedB EffectKind = "notify_accepted_b"
	EffectInjectFailure   EffectKind = "inject_failure"
	EffectNotifyFailure   EffectKind = "notify_failure"
	EffectRemoveFailure   EffectKind = "remove_failure"
	EffectRollback        EffectKind = "rollback"
	EffectNotifyRollback  EffectKind = "notify_rollback"
)

type EffectRequest struct {
	ID           string
	Kind         EffectKind
	Artifact     Artifact
	Deployment   Deployment
	Notification NotificationRequest
	Cleanup      CleanupRequest
}

type EffectResult struct {
	Deployment   Deployment
	Notification NotificationReceipt
	Fault        Fault
	Cleanup      CleanupReceipt
}

type EffectExecutor interface {
	// Apply is durable create-if-absent by request ID. Identical retries return
	// the original receipt; a conflicting request must fail without mutation.
	Apply(context.Context, EffectRequest) (EffectResult, error)
	// Reconcile reads a prior durable effect without creating it.
	Reconcile(context.Context, EffectRequest) (EffectResult, bool, error)
}

type DurableDependencies struct {
	Authority     AuthorityVerifier
	Production    ProductionSource
	Artifacts     ArtifactVerifier
	Health        HealthProbe
	Faults        FaultController
	Sessions      SessionStore
	Effects       EffectExecutor
	Clock         Clock
	StateSigner   SessionSigner
	StateVerifier SessionVerifier
}

type Clock interface{ Now() time.Time }

func RunSession(ctx context.Context, seed SessionSeed, deps DurableDependencies) (Proof, error) {
	if seed.ID == "" || deps.Sessions == nil || deps.Effects == nil || deps.Authority == nil || deps.Production == nil || deps.Artifacts == nil || deps.Health == nil || deps.Faults == nil || deps.Clock == nil || deps.StateSigner == nil || deps.StateVerifier == nil {
		return Proof{}, fmt.Errorf("durable acceptance dependencies are incomplete")
	}
	if _, exposesSigning := deps.StateVerifier.(SessionSigner); exposesSigning {
		return Proof{}, fmt.Errorf("acceptance session store verifier exposes signing authority")
	}
	if err := validateConsecutiveEvents(seed.Events[0], seed.Events[1]); err != nil {
		return Proof{}, err
	}
	seedDigest := digestSessionSeed(seed)
	initialFactory := deps.StateSigner.InitialFactory(seed)
	if initialFactory == nil {
		return Proof{}, fmt.Errorf("acceptance session initial factory is absent")
	}
	for {
		session, err := deps.Sessions.LoadOrCreate(ctx, seed, initialFactory, deps.StateVerifier)
		if err != nil {
			return Proof{}, fmt.Errorf("load acceptance session: %w", err)
		}
		if err := verifySessionState(ctx, deps.StateVerifier, session); err != nil {
			return Proof{}, fmt.Errorf("authenticate acceptance session state: %w", err)
		}
		if session.ID != seed.ID || session.SeedDigest != seedDigest || !reflect.DeepEqual(session.Events, seed.Events) {
			return Proof{}, fmt.Errorf("acceptance session seed conflicts with durable session")
		}
		if err := validateSessionPhase(session); err != nil {
			return Proof{}, fmt.Errorf("validate acceptance session phase: %w", err)
		}
		if session.Phase == SessionComplete {
			if err := validateCompleteSession(ctx, deps, session); err != nil {
				return Proof{}, err
			}
			return session.Proof, nil
		}
		if session.Phase == SessionFailed {
			if session.Proof.Cleanup.FaultID != "" {
				if err := validateCleanupReceipt(cleanupRequest(session), session.Proof.Cleanup); err != nil {
					return Proof{}, fmt.Errorf("failed acceptance cleanup proof: %w", err)
				}
			}
			return Proof{}, fmt.Errorf("acceptance session failed after cleanup: %s", session.TerminalError)
		}
		if session.Phase == SessionCleanupFailed {
			next := session
			next.Phase = SessionCleanupIntent
			if next.Phase == SessionFailed {
				next.PhaseDeadline = time.Time{}
			} else {
				next.PhaseDeadline = deps.Clock.Now().Add(5 * time.Minute)
			}
			if err := persistSessionSuccessor(ctx, deps, session, next); err != nil {
				if errors.Is(err, ErrSessionConflict) {
					continue
				}
				return Proof{}, err
			}
			continue
		}
		if !session.PhaseDeadline.IsZero() && !deps.Clock.Now().Before(session.PhaseDeadline) && session.Phase != SessionCleanupIntent {
			next := session
			next.TerminalError = fmt.Sprintf("phase %s exceeded its durable deadline", session.Phase)
			if session.Phase == SessionFaultIntent {
				result, found, err := deps.Effects.Reconcile(ctx, effectRequest(session, EffectInjectFailure))
				if err != nil {
					return Proof{}, fmt.Errorf("reconcile expired fault injection: %w", err)
				}
				if found {
					if err := validateFaultReceipt(session, result.Fault); err != nil {
						return Proof{}, fmt.Errorf("reconcile expired fault injection: %w", err)
					}
					next.Proof.Fault = result.Fault
					next.Phase = SessionCleanupIntent
				} else {
					next.Phase = SessionFailed
				}
			} else if phaseMayHaveInjectedFault(session.Phase) {
				next.Phase = SessionCleanupIntent
			} else {
				next.Phase = SessionFailed
			}
			next.PhaseDeadline = deps.Clock.Now().Add(5 * time.Minute)
			if err := persistSessionSuccessor(ctx, deps, session, next); err != nil {
				if errors.Is(err, ErrSessionConflict) {
					continue
				}
				return Proof{}, err
			}
			continue
		}
		if session.Phase == SessionCleanupIntent && !session.PhaseDeadline.IsZero() && !deps.Clock.Now().Before(session.PhaseDeadline) {
			next := session
			request := effectRequest(session, EffectRemoveFailure)
			result, found, err := deps.Effects.Reconcile(ctx, request)
			if err != nil {
				return Proof{}, fmt.Errorf("reconcile expired fault cleanup: %w", err)
			}
			if found {
				if err := validateCleanupReceipt(request.Cleanup, result.Cleanup); err != nil {
					return Proof{}, fmt.Errorf("reconcile expired fault cleanup: %w", err)
				}
				next.Proof.Cleanup = result.Cleanup
				if session.TerminalError == "" {
					next.Phase = SessionCleaned
					next.PhaseDeadline = deps.Clock.Now().Add(5 * time.Minute)
				} else {
					next.Phase = SessionFailed
					next.PhaseDeadline = time.Time{}
				}
			} else {
				next.Phase = SessionCleanupFailed
				next.PhaseDeadline = deps.Clock.Now().Add(5 * time.Minute)
				if next.TerminalError == "" {
					next.TerminalError = "fault cleanup could not be durably acknowledged before deadline"
				}
			}
			if err := persistSessionSuccessor(ctx, deps, session, next); err != nil {
				if errors.Is(err, ErrSessionConflict) {
					continue
				}
				return Proof{}, err
			}
			if !found {
				return Proof{}, fmt.Errorf("acceptance cleanup deadline exhausted: %s", next.TerminalError)
			}
			continue
		}
		phaseCtx := ctx
		cancelPhase := func() {}
		if !session.PhaseDeadline.IsZero() {
			phaseCtx, cancelPhase = context.WithTimeout(ctx, session.PhaseDeadline.Sub(deps.Clock.Now()))
		}
		if err := validateIntentAuthority(phaseCtx, deps, session); err != nil {
			cancelPhase()
			return Proof{}, fmt.Errorf("authenticate acceptance session phase: %w", err)
		}
		next, err := advanceSession(phaseCtx, session, deps)
		cancelPhase()
		if err != nil {
			return Proof{}, err
		}
		if next.Phase != SessionComplete && next.Phase != SessionFailed {
			next.PhaseDeadline = deps.Clock.Now().Add(5 * time.Minute)
		} else {
			next.PhaseDeadline = time.Time{}
		}
		if err := persistSessionSuccessor(ctx, deps, session, next); err != nil {
			if errors.Is(err, ErrSessionConflict) {
				continue
			}
			return Proof{}, fmt.Errorf("persist acceptance transition: %w", err)
		}
	}
}

func initialSession(seed SessionSeed) Session {
	return Session{
		ID:                  seed.ID,
		SeedDigest:          digestSessionSeed(seed),
		Version:             1,
		PreviousStateDigest: genesisSessionStateDigest(),
		Phase:               SessionStarted,
		Events:              seed.Events,
	}
}

func persistSessionSuccessor(ctx context.Context, deps DurableDependencies, current, next Session) error {
	next.Version = current.Version + 1
	next.PreviousStateDigest = current.StateSeal.Digest
	next.StateSeal = StateSeal{}
	sealed, err := deps.StateSigner.SealSuccessor(ctx, current, next)
	if err != nil {
		return fmt.Errorf("seal acceptance session successor: %w", err)
	}
	return deps.Sessions.CompareAndSwap(ctx, current.ID, current.Version, current.StateSeal.Digest, current.SeedDigest, sealed, deps.StateVerifier)
}

func sessionWithStateDigest(session Session) (Session, error) {
	digest, err := digestSessionState(session)
	if err != nil {
		return Session{}, err
	}
	session.StateSeal.Digest = digest
	return session, nil
}

func verifySessionState(ctx context.Context, auth SessionVerifier, session Session) error {
	digest, err := digestSessionState(session)
	if err != nil {
		return err
	}
	if session.StateSeal.Digest != digest {
		return fmt.Errorf("acceptance session state digest mismatch")
	}
	if session.StateSeal.Signature == "" {
		return fmt.Errorf("acceptance session state signature is absent")
	}
	if err := auth.Verify(ctx, session); err != nil {
		return err
	}
	return nil
}

func validateInitialSealRequest(seed SessionSeed, initial Session) error {
	if initial.ID != seed.ID || initial.SeedDigest != digestSessionSeed(seed) || initial.Events != seed.Events {
		return fmt.Errorf("initial acceptance session identity differs from seed")
	}
	if initial.Version != 1 || initial.PreviousStateDigest != genesisSessionStateDigest() || initial.Phase != SessionStarted {
		return fmt.Errorf("initial acceptance session does not start at sealed genesis")
	}
	if !reflect.DeepEqual(initial.Proof, Proof{}) || initial.TerminalError != "" || initial.PhaseDeadline.IsZero() || initial.StateSeal != (StateSeal{}) {
		return fmt.Errorf("initial acceptance session carries non-genesis state")
	}
	if err := validateSessionPhase(initial); err != nil {
		return fmt.Errorf("initial acceptance session phase: %w", err)
	}
	return nil
}

func validateSuccessorSealRequest(current, next Session, now time.Time) error {
	if err := validateSessionPhase(current); err != nil {
		return fmt.Errorf("current acceptance session phase: %w", err)
	}
	if current.ID != next.ID || current.SeedDigest != next.SeedDigest || current.Events != next.Events {
		return fmt.Errorf("acceptance session successor changes immutable identity")
	}
	if next.Version != current.Version+1 || next.PreviousStateDigest != current.StateSeal.Digest {
		return fmt.Errorf("acceptance session successor breaks monotonic digest linkage")
	}
	if next.StateSeal != (StateSeal{}) {
		return fmt.Errorf("acceptance session successor is already sealed")
	}
	if now.IsZero() || now.Before(current.StateSeal.SignedAt) {
		return fmt.Errorf("acceptance session signer clock is invalid")
	}
	rule, allowed := sessionTransitionRules[transitionEdge{from: current.Phase, to: next.Phase}]
	if !allowed {
		return fmt.Errorf("illegal acceptance session transition %s -> %s", current.Phase, next.Phase)
	}
	if err := validateProofDelta(current, next, rule); err != nil {
		return err
	}
	if next.Phase == SessionFailed && next.Proof.Fault.ID != "" && (!next.Proof.Cleanup.NoResidue || next.Proof.Cleanup.FaultID != next.Proof.Fault.ID) {
		return fmt.Errorf("acceptance session terminal state bypasses authenticated cleanup")
	}
	if current.TerminalError != "" && next.TerminalError != current.TerminalError {
		return fmt.Errorf("acceptance session successor rewrites terminal error")
	}
	if current.TerminalError == "" && next.TerminalError != "" && !rule.maySetTerminalErr {
		return fmt.Errorf("acceptance session successor introduces terminal error outside cleanup")
	}
	if next.Phase == SessionComplete || next.Phase == SessionFailed {
		if !next.PhaseDeadline.IsZero() {
			return fmt.Errorf("terminal acceptance session retains a phase deadline")
		}
		if next.Phase == SessionFailed && next.TerminalError == "" {
			return fmt.Errorf("failed acceptance session lacks a terminal error")
		}
		if next.Phase == SessionComplete && next.TerminalError != "" {
			return fmt.Errorf("completed acceptance session carries a terminal error")
		}
	} else {
		if !next.PhaseDeadline.After(now) || next.PhaseDeadline.After(now.Add(5*time.Minute)) {
			return fmt.Errorf("acceptance session successor rewrites its bounded deadline")
		}
	}
	if err := validateSessionPhase(next); err != nil {
		return fmt.Errorf("successor acceptance session phase: %w", err)
	}
	return nil
}

func validateProofDelta(current, next Session, rule transitionRule) error {
	got := changedProofFields(current.Proof, next.Proof)
	allowed := slices.Equal(got, rule.proofDelta)
	if !allowed && len(rule.optionalProof) != 0 {
		withOptional := append(append([]proofField{}, rule.proofDelta...), rule.optionalProof...)
		allowed = slices.Equal(got, withOptional)
	}
	if !allowed {
		return fmt.Errorf("acceptance session proof delta %v is not allowed for %s -> %s", got, current.Phase, next.Phase)
	}
	for _, field := range got {
		if !reflect.ValueOf(proofFieldValue(current.Proof, field)).IsZero() || reflect.ValueOf(proofFieldValue(next.Proof, field)).IsZero() {
			return fmt.Errorf("acceptance session proof field %s is not append-only", field)
		}
		if err := validateAddedProofField(next, field); err != nil {
			return err
		}
	}
	return nil
}

func changedProofFields(current, next Proof) []proofField {
	changes := make([]proofField, 0, 4)
	if !reflect.DeepEqual(current.Events, next.Events) {
		changes = append(changes, proofEvents)
	}
	arrays := []struct {
		fields [2]proofField
		old    [2]any
		new    [2]any
	}{
		{fields: [2]proofField{proofProductionA, proofProductionB}, old: [2]any{current.Production[0], current.Production[1]}, new: [2]any{next.Production[0], next.Production[1]}},
		{fields: [2]proofField{proofArtifactA, proofArtifactB}, old: [2]any{current.Artifacts[0], current.Artifacts[1]}, new: [2]any{next.Artifacts[0], next.Artifacts[1]}},
		{fields: [2]proofField{proofDeploymentA, proofDeploymentB}, old: [2]any{current.Deployments[0], current.Deployments[1]}, new: [2]any{next.Deployments[0], next.Deployments[1]}},
		{fields: [2]proofField{proofHealthA, proofHealthB}, old: [2]any{current.Health[0], current.Health[1]}, new: [2]any{next.Health[0], next.Health[1]}},
		{fields: [2]proofField{proofNotificationA, proofNotificationB}, old: [2]any{current.Notifications[0], current.Notifications[1]}, new: [2]any{next.Notifications[0], next.Notifications[1]}},
	}
	for _, pair := range arrays {
		for index := range pair.old {
			if !reflect.DeepEqual(pair.old[index], pair.new[index]) {
				changes = append(changes, pair.fields[index])
			}
		}
	}
	scalars := []struct {
		field proofField
		old   any
		new   any
	}{
		{field: proofFault, old: current.Fault, new: next.Fault},
		{field: proofFailure, old: current.Failure, new: next.Failure},
		{field: proofFailureNotice, old: current.FailureNotice, new: next.FailureNotice},
		{field: proofCleanup, old: current.Cleanup, new: next.Cleanup},
		{field: proofRollback, old: current.Rollback, new: next.Rollback},
		{field: proofRollbackHealth, old: current.RollbackHealth, new: next.RollbackHealth},
		{field: proofRollbackNotice, old: current.RollbackNotice, new: next.RollbackNotice},
	}
	for _, pair := range scalars {
		if !reflect.DeepEqual(pair.old, pair.new) {
			changes = append(changes, pair.field)
		}
	}
	return changes
}

func proofFieldValue(proof Proof, field proofField) any {
	switch field {
	case proofEvents:
		return proof.Events
	case proofProductionA:
		return proof.Production[0]
	case proofProductionB:
		return proof.Production[1]
	case proofArtifactA:
		return proof.Artifacts[0]
	case proofArtifactB:
		return proof.Artifacts[1]
	case proofDeploymentA:
		return proof.Deployments[0]
	case proofDeploymentB:
		return proof.Deployments[1]
	case proofHealthA:
		return proof.Health[0]
	case proofHealthB:
		return proof.Health[1]
	case proofNotificationA:
		return proof.Notifications[0]
	case proofNotificationB:
		return proof.Notifications[1]
	case proofFault:
		return proof.Fault
	case proofFailure:
		return proof.Failure
	case proofFailureNotice:
		return proof.FailureNotice
	case proofCleanup:
		return proof.Cleanup
	case proofRollback:
		return proof.Rollback
	case proofRollbackHealth:
		return proof.RollbackHealth
	case proofRollbackNotice:
		return proof.RollbackNotice
	default:
		panic(fmt.Sprintf("unknown acceptance proof field %d", field))
	}
}

func (field proofField) String() string {
	names := [...]string{
		"events", "production_a", "production_b", "artifact_a", "artifact_b",
		"deployment_a", "deployment_b", "health_a", "health_b", "notification_a", "notification_b",
		"fault", "failure", "failure_notice", "cleanup", "rollback", "rollback_health", "rollback_notice",
	}
	if int(field) >= len(names) {
		return fmt.Sprintf("proof_field_%d", field)
	}
	return names[field]
}

func validateAddedProofField(session Session, field proofField) error {
	switch field {
	case proofEvents:
		if session.Proof.Events != session.Events {
			return fmt.Errorf("prepared proof events differ from session events")
		}
	case proofProductionA, proofArtifactA:
		return validatePreparedProof(session, 0)
	case proofProductionB, proofArtifactB:
		return validatePreparedProof(session, 1)
	case proofDeploymentA:
		return validateDeployment(session.Proof.Artifacts[0], session.Proof.Deployments[0], session.Proof.Artifacts[0].VerifiedAt)
	case proofDeploymentB:
		return validateDeployment(session.Proof.Artifacts[1], session.Proof.Deployments[1], session.Proof.Artifacts[1].VerifiedAt)
	case proofHealthA:
		return validateHealthReceipt(session.Proof.Deployments[0], session.Proof.Health[0])
	case proofHealthB:
		return validateHealthReceipt(session.Proof.Deployments[1], session.Proof.Health[1])
	case proofNotificationA:
		return validateNotificationReceipt(acceptedNotification(session, 0), session.Proof.Notifications[0])
	case proofNotificationB:
		return validateNotificationReceipt(acceptedNotification(session, 1), session.Proof.Notifications[1])
	case proofFault:
		return validateFaultReceipt(session, session.Proof.Fault)
	case proofFailure:
		return validateFailureReceipt(session.Proof.Fault, session.Proof.Failure)
	case proofFailureNotice:
		return validateNotificationReceipt(failureNotification(session), session.Proof.FailureNotice)
	case proofCleanup:
		return validateCleanupReceipt(cleanupRequest(session), session.Proof.Cleanup)
	case proofRollback:
		if err := validateDeployment(session.Proof.Artifacts[0], session.Proof.Rollback, session.Proof.Cleanup.RemovedAt); err != nil {
			return err
		}
		if session.Proof.Rollback.UID == session.Proof.Deployments[0].UID {
			return fmt.Errorf("rollback reused original deployment identity")
		}
	case proofRollbackHealth:
		return validateHealthReceipt(session.Proof.Rollback, session.Proof.RollbackHealth)
	case proofRollbackNotice:
		return validateNotificationReceipt(rollbackNotification(session), session.Proof.RollbackNotice)
	}
	return nil
}

func validatePreparedProof(session Session, index int) error {
	event := session.Events[index]
	receipt := session.Proof.Production[index]
	artifact := session.Proof.Artifacts[index]
	if receipt.EventID != event.EventID || receipt.Commit != event.Commit || receipt.Tree != event.Tree || receipt.TerminalStage != Discord || !receipt.SuccessAt.Before(event.Deadline) {
		return fmt.Errorf("prepared production receipt %d differs from ordinary land", index)
	}
	if artifact.EventID != event.EventID || artifact.Commit != event.Commit || artifact.Tree != event.Tree || artifact.Digest != event.ArtifactManifestDigest || !artifact.VerifiedAt.After(receipt.SuccessAt) {
		return fmt.Errorf("prepared artifact receipt %d differs from ordinary land", index)
	}
	return nil
}

func digestSessionState(session Session) (string, error) {
	payload := []any{
		session.StateSeal.Domain, session.StateSeal.SchemaVersion, session.StateSeal.KeyID,
		session.StateSeal.Algorithm, timeV1(session.StateSeal.SignedAt), session.ID, session.SeedDigest,
		session.Version, session.PreviousStateDigest, string(session.Phase),
		[]any{landEventV1(session.Events[0]), landEventV1(session.Events[1])}, proofV1(session.Proof),
		session.TerminalError, timeV1(session.PhaseDeadline),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal acceptance session state: %w", err)
	}
	sum := sha256.Sum256(append([]byte("sparkwing/livechainacceptance/session/v1\x00"), raw...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func timeV1(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func landEventV1(value LandEvent) []any {
	return []any{value.EventID, value.Repository, value.DestinationRef, value.Commit, value.ParentCommit,
		value.Tree, value.CertificationID, value.ArtifactManifestDigest, value.TrustManifestDigest,
		value.SourceDigest, value.GitLedgerID, value.LandRecordID, value.LandLedgerID, value.LandSequence,
		value.ChainLedgerID, value.ChainBasePosition, timeV1(value.LandedAt), timeV1(value.Deadline)}
}

func acknowledgementV1(value Acknowledgement) []any {
	delivery := []any(nil)
	if value.DiscordDelivery != nil {
		delivery = []any{value.DiscordDelivery.BridgeIdentity, value.DiscordDelivery.RequestID,
			value.DiscordDelivery.PayloadDigest, value.DiscordDelivery.HTTPStatus, timeV1(value.DiscordDelivery.DeliveredAt)}
	}
	return []any{string(value.Stage), value.Digest, value.EventID, value.PreviousSelectedDigest,
		value.StageEvidenceDigest, timeV1(value.StageAt), value.Repository, value.DestinationRef, value.Commit,
		value.Tree, value.CertificationID, value.ArtifactManifestDigest, value.TrustManifestDigest,
		timeV1(value.LandedAt), timeV1(value.Deadline), value.AuthorityDomain, value.SignerKeyID,
		value.ImmutableVersion, value.LedgerPosition, value.Signature, delivery}
}

func authorityReceiptV1(value AuthorityReceipt) []any {
	return []any{value.Domain, value.LedgerID, value.SignerKeyID, value.ImmutableVersion,
		value.LedgerPosition, value.VerifiedDigest, value.InclusionDigest, value.Signature}
}

func productionReceiptV1(value ProductionReceipt) []any {
	acks := make([]any, len(value.Acknowledgements))
	for index := range value.Acknowledgements {
		acks[index] = acknowledgementV1(value.Acknowledgements[index])
	}
	authority := make([]any, len(value.Authority))
	for index := range value.Authority {
		authority[index] = authorityReceiptV1(value.Authority[index])
	}
	return []any{value.EventID, value.Commit, value.Tree, string(value.TerminalStage), value.TerminalDigest,
		timeV1(value.SuccessAt), int64(value.LandToSuccess), acks, authority}
}

func artifactV1(value Artifact) []any {
	return []any{value.EventID, value.Commit, value.Tree, value.Digest, timeV1(value.VerifiedAt)}
}

func deploymentV1(value Deployment) []any {
	return []any{value.EventID, value.Commit, value.Tree, value.Digest, value.UID, timeV1(value.DeployedAt)}
}

func healthReceiptV1(value HealthReceipt) []any {
	return []any{value.EventID, value.Commit, value.Tree, value.Digest, value.DeploymentUID, value.Healthy, timeV1(value.ObservedAt)}
}

func notificationRequestV1(value NotificationRequest) []any {
	return []any{string(value.Kind), value.EventID, value.Commit, value.Tree, value.Digest,
		value.DeploymentUID, value.FaultID, timeV1(value.NotBefore)}
}

func notificationReceiptV1(value NotificationReceipt) []any {
	return []any{notificationRequestV1(value.NotificationRequest), value.BridgeIdentity, value.RequestID,
		value.PayloadDigest, value.HTTPStatus, timeV1(value.DeliveredAt)}
}

func proofV1(value Proof) []any {
	return []any{
		[]any{landEventV1(value.Events[0]), landEventV1(value.Events[1])},
		[]any{productionReceiptV1(value.Production[0]), productionReceiptV1(value.Production[1])},
		[]any{artifactV1(value.Artifacts[0]), artifactV1(value.Artifacts[1])},
		[]any{deploymentV1(value.Deployments[0]), deploymentV1(value.Deployments[1])},
		[]any{healthReceiptV1(value.Health[0]), healthReceiptV1(value.Health[1])},
		[]any{notificationReceiptV1(value.Notifications[0]), notificationReceiptV1(value.Notifications[1])},
		[]any{value.Fault.EventID, value.Fault.ID, value.Fault.DeploymentUID, value.Fault.Digest, timeV1(value.Fault.InjectedAt)},
		[]any{value.Failure.FaultID, value.Failure.DeploymentUID, value.Failure.Digest, value.Failure.Unhealthy, timeV1(value.Failure.ObservedAt)},
		notificationReceiptV1(value.FailureNotice),
		[]any{value.Cleanup.FaultID, value.Cleanup.EventID, value.Cleanup.DeploymentUID, value.Cleanup.Digest, value.Cleanup.NoResidue, timeV1(value.Cleanup.RemovedAt)},
		deploymentV1(value.Rollback), healthReceiptV1(value.RollbackHealth), notificationReceiptV1(value.RollbackNotice),
	}
}

func genesisSessionStateDigest() string {
	sum := sha256.Sum256([]byte("sparkwing/livechainacceptance/session/v1\x00genesis"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateIntentAuthority(ctx context.Context, deps DurableDependencies, session Session) error {
	switch session.Phase {
	case SessionADeployIntent:
		return reverifyPreparedFlow(ctx, deps, session, 0)
	case SessionANotifyIntent:
		if err := reconcilePriorEffect(ctx, deps.Effects, session, EffectDeployA); err != nil {
			return err
		}
		return reverifyHealth(ctx, deps.Health, session.Proof.Deployments[0], session.Proof.Health[0])
	case SessionBDeployIntent:
		return reverifyPreparedFlow(ctx, deps, session, 1)
	case SessionBNotifyIntent:
		if err := reconcilePriorEffect(ctx, deps.Effects, session, EffectDeployB); err != nil {
			return err
		}
		return reverifyHealth(ctx, deps.Health, session.Proof.Deployments[1], session.Proof.Health[1])
	case SessionFaultIntent:
		return reconcilePriorEffect(ctx, deps.Effects, session, EffectNotifyAcceptedB)
	case SessionFailureNotifyIntent:
		if err := reconcilePriorEffect(ctx, deps.Effects, session, EffectInjectFailure); err != nil {
			return err
		}
		return deps.Faults.AuthenticateFailure(ctx, session.Proof.Fault, session.Proof.Failure)
	case SessionCleanupIntent:
		return authenticateCleanupPrerequisite(ctx, deps, session)
	case SessionRollbackIntent:
		if err := reconcilePriorEffect(ctx, deps.Effects, session, EffectRemoveFailure); err != nil {
			return err
		}
		return reconcilePriorEffect(ctx, deps.Effects, session, EffectDeployA)
	case SessionRollbackNotifyIntent:
		if err := reconcilePriorEffect(ctx, deps.Effects, session, EffectRollback); err != nil {
			return err
		}
		return reverifyHealth(ctx, deps.Health, session.Proof.Rollback, session.Proof.RollbackHealth)
	default:
		return nil
	}
}

func authenticateCleanupPrerequisite(ctx context.Context, deps DurableDependencies, session Session) error {
	if session.Proof.FailureNotice.RequestID != "" {
		return reconcilePriorEffect(ctx, deps.Effects, session, EffectNotifyFailure)
	}
	if session.Proof.Fault.ID != "" {
		return reconcilePriorEffect(ctx, deps.Effects, session, EffectInjectFailure)
	}
	return reconcilePriorEffect(ctx, deps.Effects, session, EffectNotifyAcceptedB)
}

func reverifyPreparedFlow(ctx context.Context, deps DurableDependencies, session Session, index int) error {
	event := session.Events[index]
	stored := session.Proof.Production[index]
	verified, err := VerifySelectedChain(ctx, deps.Authority, event, stored.Acknowledgements)
	if err != nil {
		return fmt.Errorf("reverify production chain %d: %w", index, err)
	}
	if !reflect.DeepEqual(verified, stored) {
		return fmt.Errorf("production chain %d differs from durable session", index)
	}
	if err := deps.Artifacts.AuthenticateArtifact(ctx, event, session.Proof.Artifacts[index]); err != nil {
		return fmt.Errorf("authenticate artifact %d: %w", index, err)
	}
	return nil
}

func reverifyHealth(ctx context.Context, health HealthProbe, deployment Deployment, stored HealthReceipt) error {
	return health.AuthenticateHealth(ctx, deployment, stored)
}

func reconcilePriorEffect(ctx context.Context, effects EffectExecutor, session Session, kind EffectKind) error {
	request := effectRequest(session, kind)
	result, found, err := effects.Reconcile(ctx, request)
	if err != nil {
		return fmt.Errorf("reconcile prerequisite effect %s: %w", kind, err)
	}
	if !found {
		return fmt.Errorf("prerequisite effect %s is absent", kind)
	}
	if !reflect.DeepEqual(result, completedEffectResult(session, kind)) {
		return fmt.Errorf("prerequisite effect %s differs from durable session", kind)
	}
	return nil
}

func validateSessionPhase(session Session) error {
	level, ok := sessionPhaseLevel(session.Phase)
	if !ok {
		return fmt.Errorf("unknown acceptance session phase %q", session.Phase)
	}
	present := proofPresence(session)
	if session.Phase == SessionCleanupIntent || session.Phase == SessionCleanupFailed || session.Phase == SessionFailed {
		level = populatedProofPrefix(session)
		if session.Phase != SessionFailed && level < 8 {
			return fmt.Errorf("phase %s lacks the accepted second deployment", session.Phase)
		}
	}
	for index, populated := range present {
		required := index < level
		if populated != required {
			return fmt.Errorf("phase %s proof field %d populated=%t, want %t", session.Phase, index, populated, required)
		}
	}
	if level == 0 {
		if session.Proof.Events != [2]LandEvent{} {
			return fmt.Errorf("phase %s carries proof events before preparation", session.Phase)
		}
	} else if session.Proof.Events != session.Events {
		return fmt.Errorf("phase %s proof events differ from session events", session.Phase)
	}
	if session.Phase != SessionCleanupIntent && session.Phase != SessionFailed && session.Phase != SessionCleanupFailed && session.TerminalError != "" {
		return fmt.Errorf("phase %s carries a terminal error", session.Phase)
	}
	return nil
}

func proofPresence(session Session) []bool {
	return []bool{
		session.Proof.Production[0].EventID != "" && session.Proof.Artifacts[0].EventID != "",
		session.Proof.Deployments[0].UID != "",
		session.Proof.Health[0].DeploymentUID != "",
		session.Proof.Notifications[0].RequestID != "",
		session.Proof.Production[1].EventID != "" && session.Proof.Artifacts[1].EventID != "",
		session.Proof.Deployments[1].UID != "",
		session.Proof.Health[1].DeploymentUID != "",
		session.Proof.Notifications[1].RequestID != "",
		session.Proof.Fault.ID != "",
		session.Proof.Failure.FaultID != "",
		session.Proof.FailureNotice.RequestID != "",
		session.Proof.Cleanup.FaultID != "",
		session.Proof.Rollback.UID != "",
		session.Proof.RollbackHealth.DeploymentUID != "",
		session.Proof.RollbackNotice.RequestID != "",
	}
}

func populatedProofPrefix(session Session) int {
	present := proofPresence(session)
	level := 0
	for level < len(present) && present[level] {
		level++
	}
	return level
}

func sessionPhaseLevel(phase SessionPhase) (int, bool) {
	levels := map[SessionPhase]int{
		SessionStarted:   0,
		SessionAPrepared: 1, SessionADeployIntent: 1,
		SessionADeployed: 2,
		SessionAHealthy:  3, SessionANotifyIntent: 3,
		SessionAAccepted: 4,
		SessionBPrepared: 5, SessionBDeployIntent: 5,
		SessionBDeployed: 6,
		SessionBHealthy:  7, SessionBNotifyIntent: 7,
		SessionBAccepted: 8, SessionFaultIntent: 8,
		SessionFaultInjected:   9,
		SessionFailureObserved: 10, SessionFailureNotifyIntent: 10,
		SessionFailureNotified: 11,
		SessionCleanupIntent:   11,
		SessionCleaned:         12, SessionRollbackIntent: 12,
		SessionRolledBack:      13,
		SessionRollbackHealthy: 14, SessionRollbackNotifyIntent: 14,
		SessionComplete:      15,
		SessionFailed:        12,
		SessionCleanupFailed: 11,
	}
	level, ok := levels[phase]
	return level, ok
}

func phaseMayHaveInjectedFault(phase SessionPhase) bool {
	switch phase {
	case SessionFaultIntent, SessionFaultInjected, SessionFailureObserved, SessionFailureNotifyIntent, SessionFailureNotified:
		return true
	default:
		return false
	}
}

func digestSessionSeed(seed SessionSeed) string {
	raw, err := json.Marshal(seed)
	if err != nil {
		panic(fmt.Sprintf("marshal acceptance session seed: %v", err))
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateCompleteSession(ctx context.Context, deps DurableDependencies, session Session) error {
	if session.Proof.Events != session.Events {
		return fmt.Errorf("completed acceptance proof events differ from session seed")
	}
	for index, event := range session.Events {
		stored := session.Proof.Production[index]
		verified, err := VerifySelectedChain(ctx, deps.Authority, event, stored.Acknowledgements)
		if err != nil {
			return fmt.Errorf("reverify completed production chain %d: %w", index, err)
		}
		if !reflect.DeepEqual(verified, stored) {
			return fmt.Errorf("completed production receipt %d differs from authenticated chain", index)
		}
		artifact := session.Proof.Artifacts[index]
		if artifact.EventID != event.EventID || artifact.Commit != event.Commit || artifact.Tree != event.Tree || artifact.Digest != event.ArtifactManifestDigest || !artifact.VerifiedAt.After(stored.SuccessAt) {
			return fmt.Errorf("completed artifact %d differs from production identity", index)
		}
		if err := deps.Artifacts.AuthenticateArtifact(ctx, event, artifact); err != nil {
			return fmt.Errorf("reauthenticate completed artifact %d: %w", index, err)
		}
		deployment := session.Proof.Deployments[index]
		if err := validateDeployment(artifact, deployment, artifact.VerifiedAt); err != nil {
			return err
		}
		if err := validateHealthReceipt(deployment, session.Proof.Health[index]); err != nil {
			return err
		}
		if err := deps.Health.AuthenticateHealth(ctx, deployment, session.Proof.Health[index]); err != nil {
			return fmt.Errorf("reauthenticate completed health %d: %w", index, err)
		}
		if err := validateNotificationReceipt(acceptedNotification(session, index), session.Proof.Notifications[index]); err != nil {
			return err
		}
	}
	if err := validateFailureReceipt(session.Proof.Fault, session.Proof.Failure); err != nil {
		return err
	}
	if err := deps.Faults.AuthenticateFailure(ctx, session.Proof.Fault, session.Proof.Failure); err != nil {
		return fmt.Errorf("reauthenticate completed failure: %w", err)
	}
	if err := validateNotificationReceipt(failureNotification(session), session.Proof.FailureNotice); err != nil {
		return err
	}
	if err := validateCleanupReceipt(cleanupRequest(session), session.Proof.Cleanup); err != nil {
		return err
	}
	if err := validateDeployment(session.Proof.Artifacts[0], session.Proof.Rollback, session.Proof.Cleanup.RemovedAt); err != nil {
		return err
	}
	if session.Proof.Rollback.UID == session.Proof.Deployments[0].UID {
		return fmt.Errorf("completed rollback reused original deployment identity")
	}
	if err := validateHealthReceipt(session.Proof.Rollback, session.Proof.RollbackHealth); err != nil {
		return err
	}
	if err := deps.Health.AuthenticateHealth(ctx, session.Proof.Rollback, session.Proof.RollbackHealth); err != nil {
		return fmt.Errorf("reauthenticate completed rollback health: %w", err)
	}
	if err := validateNotificationReceipt(rollbackNotification(session), session.Proof.RollbackNotice); err != nil {
		return err
	}
	return reconcileCompletedEffects(ctx, deps.Effects, session)
}

func reconcileCompletedEffects(ctx context.Context, effects EffectExecutor, session Session) error {
	for _, kind := range []EffectKind{
		EffectDeployA, EffectNotifyAcceptedA, EffectDeployB, EffectNotifyAcceptedB,
		EffectInjectFailure, EffectNotifyFailure, EffectRemoveFailure, EffectRollback, EffectNotifyRollback,
	} {
		request := effectRequest(session, kind)
		result, found, err := effects.Reconcile(ctx, request)
		if err != nil {
			return fmt.Errorf("reconcile completed effect %s: %w", kind, err)
		}
		if !found {
			return fmt.Errorf("completed effect %s is absent from durable authority", kind)
		}
		if !reflect.DeepEqual(result, completedEffectResult(session, kind)) {
			return fmt.Errorf("completed effect %s differs from durable authority", kind)
		}
	}
	return nil
}

func completedEffectResult(session Session, kind EffectKind) EffectResult {
	switch kind {
	case EffectDeployA:
		return EffectResult{Deployment: session.Proof.Deployments[0]}
	case EffectNotifyAcceptedA:
		return EffectResult{Notification: session.Proof.Notifications[0]}
	case EffectDeployB:
		return EffectResult{Deployment: session.Proof.Deployments[1]}
	case EffectNotifyAcceptedB:
		return EffectResult{Notification: session.Proof.Notifications[1]}
	case EffectInjectFailure:
		return EffectResult{Fault: session.Proof.Fault}
	case EffectNotifyFailure:
		return EffectResult{Notification: session.Proof.FailureNotice}
	case EffectRemoveFailure:
		return EffectResult{Cleanup: session.Proof.Cleanup}
	case EffectRollback:
		return EffectResult{Deployment: session.Proof.Rollback}
	case EffectNotifyRollback:
		return EffectResult{Notification: session.Proof.RollbackNotice}
	default:
		panic(fmt.Sprintf("unknown completed effect %q", kind))
	}
}

func advanceSession(ctx context.Context, session Session, deps DurableDependencies) (Session, error) {
	next := session
	switch session.Phase {
	case SessionStarted:
		if err := prepareFlow(ctx, deps, &next, 0); err != nil {
			return Session{}, err
		}
		next.Phase = SessionAPrepared
	case SessionAPrepared:
		next.Phase = SessionADeployIntent
	case SessionADeployIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectDeployA))
		if err != nil {
			return Session{}, fmt.Errorf("apply deploy A: %w", err)
		}
		if err := validateDeployment(session.Proof.Artifacts[0], result.Deployment, session.Proof.Artifacts[0].VerifiedAt); err != nil {
			return Session{}, err
		}
		next.Proof.Deployments[0] = result.Deployment
		next.Phase = SessionADeployed
	case SessionADeployed:
		health, err := deps.Health.Healthy(ctx, session.Proof.Deployments[0])
		if err != nil {
			return Session{}, err
		}
		if err := validateHealthReceipt(session.Proof.Deployments[0], health); err != nil {
			return Session{}, err
		}
		next.Proof.Health[0] = health
		next.Phase = SessionAHealthy
	case SessionAHealthy:
		next.Phase = SessionANotifyIntent
	case SessionANotifyIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectNotifyAcceptedA))
		if err != nil {
			return Session{}, fmt.Errorf("apply accepted notification A: %w", err)
		}
		request := acceptedNotification(session, 0)
		if err := validateNotificationReceipt(request, result.Notification); err != nil {
			return Session{}, err
		}
		next.Proof.Notifications[0] = result.Notification
		next.Phase = SessionAAccepted
	case SessionAAccepted:
		if err := prepareFlow(ctx, deps, &next, 1); err != nil {
			return Session{}, err
		}
		next.Phase = SessionBPrepared
	case SessionBPrepared:
		next.Phase = SessionBDeployIntent
	case SessionBDeployIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectDeployB))
		if err != nil {
			return Session{}, fmt.Errorf("apply deploy B: %w", err)
		}
		if err := validateDeployment(session.Proof.Artifacts[1], result.Deployment, session.Proof.Artifacts[1].VerifiedAt); err != nil {
			return Session{}, err
		}
		next.Proof.Deployments[1] = result.Deployment
		next.Phase = SessionBDeployed
	case SessionBDeployed:
		health, err := deps.Health.Healthy(ctx, session.Proof.Deployments[1])
		if err != nil {
			return Session{}, err
		}
		if err := validateHealthReceipt(session.Proof.Deployments[1], health); err != nil {
			return Session{}, err
		}
		next.Proof.Health[1] = health
		next.Phase = SessionBHealthy
	case SessionBHealthy:
		next.Phase = SessionBNotifyIntent
	case SessionBNotifyIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectNotifyAcceptedB))
		if err != nil {
			return Session{}, fmt.Errorf("apply accepted notification B: %w", err)
		}
		request := acceptedNotification(session, 1)
		if err := validateNotificationReceipt(request, result.Notification); err != nil {
			return Session{}, err
		}
		next.Proof.Notifications[1] = result.Notification
		next.Phase = SessionBAccepted
	case SessionBAccepted:
		next.Phase = SessionFaultIntent
	case SessionFaultIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectInjectFailure))
		if err != nil {
			return Session{}, fmt.Errorf("apply fault injection: %w", err)
		}
		if err := validateFaultReceipt(session, result.Fault); err != nil {
			next.TerminalError = err.Error()
			next.Phase = SessionCleanupIntent
			return next, nil
		}
		next.Proof.Fault = result.Fault
		next.Phase = SessionFaultInjected
	case SessionFaultInjected:
		failure, err := deps.Faults.ObserveFailure(ctx, session.Proof.Fault)
		if err != nil {
			return Session{}, err
		}
		if err := validateFailureReceipt(session.Proof.Fault, failure); err != nil {
			return Session{}, err
		}
		next.Proof.Failure = failure
		next.Phase = SessionFailureObserved
	case SessionFailureObserved:
		next.Phase = SessionFailureNotifyIntent
	case SessionFailureNotifyIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectNotifyFailure))
		if err != nil {
			return Session{}, fmt.Errorf("apply failure notification: %w", err)
		}
		request := failureNotification(session)
		if err := validateNotificationReceipt(request, result.Notification); err != nil {
			return Session{}, err
		}
		next.Proof.FailureNotice = result.Notification
		next.Phase = SessionFailureNotified
	case SessionFailureNotified:
		next.Phase = SessionCleanupIntent
	case SessionCleanupIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectRemoveFailure))
		if err != nil {
			return Session{}, fmt.Errorf("apply fault cleanup: %w", err)
		}
		request := cleanupRequest(session)
		if err := validateCleanupReceipt(request, result.Cleanup); err != nil {
			return Session{}, err
		}
		next.Proof.Cleanup = result.Cleanup
		if session.TerminalError != "" {
			next.Phase = SessionFailed
		} else {
			next.Phase = SessionCleaned
		}
	case SessionCleaned:
		next.Phase = SessionRollbackIntent
	case SessionRollbackIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectRollback))
		if err != nil {
			return Session{}, fmt.Errorf("apply rollback: %w", err)
		}
		if err := validateDeployment(session.Proof.Artifacts[0], result.Deployment, session.Proof.Cleanup.RemovedAt); err != nil {
			return Session{}, err
		}
		if result.Deployment.UID == session.Proof.Deployments[0].UID {
			return Session{}, fmt.Errorf("rollback did not create a new deployment")
		}
		next.Proof.Rollback = result.Deployment
		next.Phase = SessionRolledBack
	case SessionRolledBack:
		health, err := deps.Health.Healthy(ctx, session.Proof.Rollback)
		if err != nil {
			return Session{}, err
		}
		if err := validateHealthReceipt(session.Proof.Rollback, health); err != nil {
			return Session{}, err
		}
		next.Proof.RollbackHealth = health
		next.Phase = SessionRollbackHealthy
	case SessionRollbackHealthy:
		next.Phase = SessionRollbackNotifyIntent
	case SessionRollbackNotifyIntent:
		result, err := deps.Effects.Apply(ctx, effectRequest(session, EffectNotifyRollback))
		if err != nil {
			return Session{}, fmt.Errorf("apply rollback notification: %w", err)
		}
		request := rollbackNotification(session)
		if err := validateNotificationReceipt(request, result.Notification); err != nil {
			return Session{}, err
		}
		next.Proof.RollbackNotice = result.Notification
		next.Phase = SessionComplete
	default:
		return Session{}, fmt.Errorf("unknown acceptance session phase %q", session.Phase)
	}
	return next, nil
}

func validateFaultReceipt(session Session, fault Fault) error {
	deployment := session.Proof.Deployments[1]
	if fault.ID != effectID(session.ID, EffectInjectFailure) || fault.EventID != deployment.EventID || fault.DeploymentUID != deployment.UID || fault.Digest != deployment.Digest || !fault.InjectedAt.After(session.Proof.Notifications[1].DeliveredAt) {
		return fmt.Errorf("durable fault receipt differs from requested deployment")
	}
	return nil
}

func prepareFlow(ctx context.Context, deps DurableDependencies, session *Session, index int) error {
	event := session.Events[index]
	chain, err := deps.Production.SelectedChain(ctx, event)
	if err != nil {
		return err
	}
	receipt, err := VerifySelectedChain(ctx, deps.Authority, event, chain)
	if err != nil {
		return err
	}
	artifact, err := deps.Artifacts.VerifyArtifact(ctx, event)
	if err != nil {
		return err
	}
	if artifact.EventID != event.EventID || artifact.Commit != event.Commit || artifact.Tree != event.Tree || artifact.Digest != event.ArtifactManifestDigest || !artifact.VerifiedAt.After(receipt.SuccessAt) {
		return fmt.Errorf("verified artifact identity differs from ordinary land %s", event.Commit)
	}
	session.Proof.Events = session.Events
	session.Proof.Production[index] = receipt
	session.Proof.Artifacts[index] = artifact
	return nil
}

func effectID(sessionID string, kind EffectKind) string { return sessionID + "/" + string(kind) }

func effectRequest(session Session, kind EffectKind) EffectRequest {
	request := EffectRequest{ID: effectID(session.ID, kind), Kind: kind}
	switch kind {
	case EffectDeployA:
		request.Artifact = session.Proof.Artifacts[0]
	case EffectDeployB:
		request.Artifact = session.Proof.Artifacts[1]
	case EffectNotifyAcceptedA:
		request.Notification = acceptedNotification(session, 0)
	case EffectNotifyAcceptedB:
		request.Notification = acceptedNotification(session, 1)
	case EffectInjectFailure:
		request.Deployment = session.Proof.Deployments[1]
	case EffectNotifyFailure:
		request.Notification = failureNotification(session)
	case EffectRemoveFailure:
		request.Cleanup = cleanupRequest(session)
	case EffectRollback:
		request.Deployment = session.Proof.Deployments[0]
	case EffectNotifyRollback:
		request.Notification = rollbackNotification(session)
	}
	return request
}

func acceptedNotification(session Session, index int) NotificationRequest {
	return notificationRequest(AcceptedNotification, session.Proof.Deployments[index], "", session.Proof.Health[index].ObservedAt)
}

func failureNotification(session Session) NotificationRequest {
	return notificationRequest(FailureNotification, session.Proof.Deployments[1], session.Proof.Fault.ID, session.Proof.Failure.ObservedAt)
}

func cleanupRequest(session Session) CleanupRequest {
	deployment := session.Proof.Deployments[1]
	notBefore := session.Proof.FailureNotice.DeliveredAt
	if notBefore.IsZero() {
		notBefore = session.Proof.Notifications[1].DeliveredAt
	}
	return CleanupRequest{FaultID: effectID(session.ID, EffectInjectFailure), EventID: deployment.EventID, DeploymentUID: deployment.UID, Digest: deployment.Digest, NotBefore: notBefore}
}

func rollbackNotification(session Session) NotificationRequest {
	return notificationRequest(RollbackNotification, session.Proof.Rollback, session.Proof.Fault.ID, session.Proof.RollbackHealth.ObservedAt)
}
