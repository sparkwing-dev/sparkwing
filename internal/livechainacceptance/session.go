package livechainacceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	ID            string
	SeedDigest    string
	Version       uint64
	Phase         SessionPhase
	Events        [2]LandEvent
	Proof         Proof
	TerminalError string
	PhaseDeadline time.Time
}

type SessionStore interface {
	LoadOrCreate(context.Context, SessionSeed) (Session, error)
	CompareAndSwap(context.Context, string, uint64, string, Session) error
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
	Authority  AuthorityVerifier
	Production ProductionSource
	Artifacts  ArtifactVerifier
	Health     HealthProbe
	Faults     FaultController
	Sessions   SessionStore
	Effects    EffectExecutor
	Clock      Clock
}

type Clock interface{ Now() time.Time }

func RunSession(ctx context.Context, seed SessionSeed, deps DurableDependencies) (Proof, error) {
	if seed.ID == "" || deps.Sessions == nil || deps.Effects == nil || deps.Authority == nil || deps.Production == nil || deps.Artifacts == nil || deps.Health == nil || deps.Faults == nil || deps.Clock == nil {
		return Proof{}, fmt.Errorf("durable acceptance dependencies are incomplete")
	}
	if err := validateConsecutiveEvents(seed.Events[0], seed.Events[1]); err != nil {
		return Proof{}, err
	}
	seedDigest := digestSessionSeed(seed)
	for {
		session, err := deps.Sessions.LoadOrCreate(ctx, seed)
		if err != nil {
			return Proof{}, fmt.Errorf("load acceptance session: %w", err)
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
			return Proof{}, fmt.Errorf("acceptance cleanup deadline exhausted: %s", session.TerminalError)
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
			if err := deps.Sessions.CompareAndSwap(ctx, session.ID, session.Version, session.SeedDigest, next); err != nil {
				if errors.Is(err, ErrSessionConflict) {
					continue
				}
				return Proof{}, err
			}
			continue
		}
		if session.Phase == SessionCleanupIntent && !session.PhaseDeadline.IsZero() && !deps.Clock.Now().Before(session.PhaseDeadline) {
			next := session
			next.Phase = SessionCleanupFailed
			next.TerminalError = "fault cleanup could not be durably acknowledged before deadline"
			if err := deps.Sessions.CompareAndSwap(ctx, session.ID, session.Version, session.SeedDigest, next); err != nil {
				if errors.Is(err, ErrSessionConflict) {
					continue
				}
				return Proof{}, err
			}
			continue
		}
		next, err := advanceSession(ctx, session, deps)
		if err != nil {
			return Proof{}, err
		}
		if next.Phase != SessionComplete && next.Phase != SessionFailed {
			next.PhaseDeadline = deps.Clock.Now().Add(5 * time.Minute)
		}
		if err := deps.Sessions.CompareAndSwap(ctx, session.ID, session.Version, session.SeedDigest, next); err != nil {
			if errors.Is(err, ErrSessionConflict) {
				continue
			}
			return Proof{}, fmt.Errorf("persist acceptance transition: %w", err)
		}
	}
}

func validateSessionPhase(session Session) error {
	level, ok := sessionPhaseLevel(session.Phase)
	if !ok {
		return fmt.Errorf("unknown acceptance session phase %q", session.Phase)
	}
	present := []bool{
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
	if session.Phase == SessionCleanupIntent || session.Phase == SessionCleanupFailed || session.Phase == SessionFailed {
		level = 0
		for level < len(present) && present[level] {
			level++
		}
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
		deployment := session.Proof.Deployments[index]
		if err := validateDeployment(artifact, deployment, artifact.VerifiedAt); err != nil {
			return err
		}
		if err := validateHealthReceipt(deployment, session.Proof.Health[index]); err != nil {
			return err
		}
		if err := validateNotificationReceipt(acceptedNotification(session, index), session.Proof.Notifications[index]); err != nil {
			return err
		}
	}
	if err := validateFailureReceipt(session.Proof.Fault, session.Proof.Failure); err != nil {
		return err
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
		wantID := effectID(session.ID, EffectInjectFailure)
		deployment := session.Proof.Deployments[1]
		if result.Fault.ID != wantID || result.Fault.EventID != deployment.EventID || result.Fault.DeploymentUID != deployment.UID || result.Fault.Digest != deployment.Digest || !result.Fault.InjectedAt.After(session.Proof.Notifications[1].DeliveredAt) {
			next.Proof.Fault = result.Fault
			next.TerminalError = "durable fault receipt differs from requested deployment"
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
