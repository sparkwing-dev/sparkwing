package livechainacceptance

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Artifact struct {
	EventID    string
	Commit     string
	Tree       string
	Digest     string
	VerifiedAt time.Time
}

type Deployment struct {
	EventID    string
	Commit     string
	Tree       string
	Digest     string
	UID        string
	DeployedAt time.Time
}

type HealthReceipt struct {
	EventID       string
	Commit        string
	Tree          string
	Digest        string
	DeploymentUID string
	Healthy       bool
	ObservedAt    time.Time
}

type Fault struct {
	EventID       string
	ID            string
	DeploymentUID string
	Digest        string
	InjectedAt    time.Time
}

type FailureReceipt struct {
	FaultID       string
	DeploymentUID string
	Digest        string
	Unhealthy     bool
	ObservedAt    time.Time
}

type CleanupReceipt struct {
	FaultID       string
	EventID       string
	DeploymentUID string
	Digest        string
	NoResidue     bool
	RemovedAt     time.Time
}

type CleanupRequest struct {
	FaultID       string
	EventID       string
	DeploymentUID string
	Digest        string
	NotBefore     time.Time
}

type NotificationKind string

const (
	AcceptedNotification NotificationKind = "accepted"
	FailureNotification  NotificationKind = "failure"
	RollbackNotification NotificationKind = "rollback"
)

type NotificationRequest struct {
	Kind          NotificationKind
	EventID       string
	Commit        string
	Tree          string
	Digest        string
	DeploymentUID string
	FaultID       string
	NotBefore     time.Time
}

type NotificationReceipt struct {
	NotificationRequest
	BridgeIdentity string
	RequestID      string
	PayloadDigest  string
	HTTPStatus     int
	DeliveredAt    time.Time
}

type MainSource interface {
	Next(context.Context) (LandEvent, error)
}

type ProductionSource interface {
	SelectedChain(context.Context, LandEvent) ([]Acknowledgement, error)
}

type ArtifactVerifier interface {
	VerifyArtifact(context.Context, LandEvent) (Artifact, error)
}

type AcceptanceDeployer interface {
	Deploy(context.Context, Artifact) (Deployment, error)
	Rollback(context.Context, Deployment) (Deployment, error)
}

type HealthProbe interface {
	Healthy(context.Context, Deployment) (HealthReceipt, error)
}

type Notifier interface {
	Notify(context.Context, NotificationRequest) (NotificationReceipt, error)
}

type FaultController interface {
	InjectFailure(context.Context, Deployment) (Fault, error)
	ObserveFailure(context.Context, Fault) (FailureReceipt, error)
	RemoveFailure(context.Context, CleanupRequest) (CleanupReceipt, error)
}

type Dependencies struct {
	Main          MainSource
	Authority     AuthorityVerifier
	Production    ProductionSource
	Artifacts     ArtifactVerifier
	Deployments   AcceptanceDeployer
	Health        HealthProbe
	Notifications Notifier
	Faults        FaultController
}

type Proof struct {
	Events         [2]LandEvent
	Production     [2]ProductionReceipt
	Artifacts      [2]Artifact
	Deployments    [2]Deployment
	Health         [2]HealthReceipt
	Notifications  [2]NotificationReceipt
	Fault          Fault
	Failure        FailureReceipt
	FailureNotice  NotificationReceipt
	Cleanup        CleanupReceipt
	Rollback       Deployment
	RollbackHealth HealthReceipt
	RollbackNotice NotificationReceipt
}

func RunTwoOrdinaryFlows(ctx context.Context, deps Dependencies) (proof Proof, returnErr error) {
	if deps.Main == nil {
		return Proof{}, fmt.Errorf("acceptance main source is nil")
	}
	a, err := deps.Main.Next(ctx)
	if err != nil {
		return Proof{}, fmt.Errorf("read first ordinary land: %w", err)
	}
	b, err := deps.Main.Next(ctx)
	if err != nil {
		return Proof{}, fmt.Errorf("read second ordinary land: %w", err)
	}
	if err := validateConsecutiveEvents(a, b); err != nil {
		return Proof{}, err
	}
	if deps.Authority == nil || deps.Production == nil || deps.Artifacts == nil || deps.Deployments == nil || deps.Health == nil || deps.Notifications == nil || deps.Faults == nil {
		return Proof{}, fmt.Errorf("acceptance dependencies are incomplete")
	}

	proof = Proof{Events: [2]LandEvent{a, b}}
	for index, event := range proof.Events {
		chain, err := deps.Production.SelectedChain(ctx, event)
		if err != nil {
			return Proof{}, fmt.Errorf("load production chain for %s: %w", event.Commit, err)
		}
		receipt, err := VerifySelectedChain(ctx, deps.Authority, event, chain)
		if err != nil {
			return Proof{}, fmt.Errorf("verify production chain for %s: %w", event.Commit, err)
		}
		artifact, err := deps.Artifacts.VerifyArtifact(ctx, event)
		if err != nil {
			return Proof{}, fmt.Errorf("verify artifact for %s: %w", event.Commit, err)
		}
		if artifact.EventID != event.EventID || artifact.Commit != event.Commit || artifact.Tree != event.Tree || artifact.Digest != event.ArtifactManifestDigest || !artifact.VerifiedAt.After(receipt.SuccessAt) {
			return Proof{}, fmt.Errorf("verified artifact identity differs from ordinary land %s", event.Commit)
		}
		deployment, err := deps.Deployments.Deploy(ctx, artifact)
		if err != nil {
			return Proof{}, fmt.Errorf("deploy acceptance artifact %s: %w", event.Commit, err)
		}
		if err := validateDeployment(artifact, deployment, artifact.VerifiedAt); err != nil {
			return Proof{}, err
		}
		health, err := deps.Health.Healthy(ctx, deployment)
		if err != nil {
			return Proof{}, fmt.Errorf("acceptance health %s: %w", event.Commit, err)
		}
		if err := validateHealthReceipt(deployment, health); err != nil {
			return Proof{}, err
		}
		noticeRequest := notificationRequest(AcceptedNotification, deployment, "", health.ObservedAt)
		notice, err := deps.Notifications.Notify(ctx, noticeRequest)
		if err != nil {
			return Proof{}, fmt.Errorf("acceptance notification %s: %w", event.Commit, err)
		}
		if err := validateNotificationReceipt(noticeRequest, notice); err != nil {
			return Proof{}, err
		}
		proof.Production[index] = receipt
		proof.Artifacts[index] = artifact
		proof.Deployments[index] = deployment
		proof.Health[index] = health
		proof.Notifications[index] = notice
	}

	fault, err := deps.Faults.InjectFailure(ctx, proof.Deployments[1])
	if err != nil {
		return Proof{}, fmt.Errorf("inject second-flow failure: %w", err)
	}
	cleanupRequest := CleanupRequest{FaultID: fault.ID, EventID: proof.Deployments[1].EventID, DeploymentUID: proof.Deployments[1].UID, Digest: proof.Deployments[1].Digest, NotBefore: fault.InjectedAt}
	cleanupPending := true
	defer func() {
		if !cleanupPending {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanup, err := deps.Faults.RemoveFailure(cleanupCtx, cleanupRequest)
		if err == nil {
			err = validateCleanupReceipt(cleanupRequest, cleanup)
		}
		if err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove injected failure during unwind: %w", err))
		}
	}()
	if fault.EventID != proof.Deployments[1].EventID || fault.ID == "" || fault.DeploymentUID != proof.Deployments[1].UID || fault.Digest != proof.Deployments[1].Digest || !fault.InjectedAt.After(proof.Notifications[1].DeliveredAt) {
		return Proof{}, fmt.Errorf("fault identity differs from second deployment")
	}
	failure, err := deps.Faults.ObserveFailure(ctx, fault)
	if err != nil {
		return Proof{}, fmt.Errorf("observe injected failure: %w", err)
	}
	if err := validateFailureReceipt(fault, failure); err != nil {
		return Proof{}, err
	}
	failureRequest := notificationRequest(FailureNotification, proof.Deployments[1], fault.ID, failure.ObservedAt)
	failureNotice, err := deps.Notifications.Notify(ctx, failureRequest)
	if err != nil {
		return Proof{}, fmt.Errorf("failure notification: %w", err)
	}
	if err := validateNotificationReceipt(failureRequest, failureNotice); err != nil {
		return Proof{}, err
	}
	cleanupRequest.NotBefore = failureNotice.DeliveredAt
	cleanup, err := deps.Faults.RemoveFailure(ctx, cleanupRequest)
	if err != nil {
		return Proof{}, fmt.Errorf("remove injected failure: %w", err)
	}
	if err := validateCleanupReceipt(cleanupRequest, cleanup); err != nil {
		return Proof{}, err
	}
	cleanupPending = false
	rollback, err := deps.Deployments.Rollback(ctx, proof.Deployments[0])
	if err != nil {
		return Proof{}, fmt.Errorf("rollback first deployment: %w", err)
	}
	if err := validateDeployment(proof.Artifacts[0], rollback, cleanup.RemovedAt); err != nil {
		return Proof{}, fmt.Errorf("rollback identity: %w", err)
	}
	if rollback.UID == proof.Deployments[0].UID {
		return Proof{}, fmt.Errorf("rollback did not produce a new deployment identity")
	}
	rollbackHealth, err := deps.Health.Healthy(ctx, rollback)
	if err != nil {
		return Proof{}, fmt.Errorf("rollback health: %w", err)
	}
	if err := validateHealthReceipt(rollback, rollbackHealth); err != nil {
		return Proof{}, err
	}
	rollbackRequest := notificationRequest(RollbackNotification, rollback, fault.ID, rollbackHealth.ObservedAt)
	rollbackNotice, err := deps.Notifications.Notify(ctx, rollbackRequest)
	if err != nil {
		return Proof{}, fmt.Errorf("rollback notification: %w", err)
	}
	if err := validateNotificationReceipt(rollbackRequest, rollbackNotice); err != nil {
		return Proof{}, err
	}
	proof.Fault = fault
	proof.Failure = failure
	proof.FailureNotice = failureNotice
	proof.Cleanup = cleanup
	proof.Rollback = rollback
	proof.RollbackHealth = rollbackHealth
	proof.RollbackNotice = rollbackNotice
	return proof, nil
}

func validateConsecutiveEvents(a, b LandEvent) error {
	if err := validateLandEvent(a); err != nil {
		return fmt.Errorf("first ordinary land: %w", err)
	}
	if err := validateLandEvent(b); err != nil {
		return fmt.Errorf("second ordinary land: %w", err)
	}
	if a.EventID == b.EventID || a.Commit == b.Commit || a.GitLedgerID == b.GitLedgerID || a.LandRecordID == b.LandRecordID {
		return fmt.Errorf("ordinary land identities are not distinct")
	}
	if b.ParentCommit != a.Commit || b.LandLedgerID != a.LandLedgerID || b.LandSequence != a.LandSequence+1 || !b.LandedAt.After(a.LandedAt) {
		return fmt.Errorf("ordinary main commits are not directly consecutive")
	}
	if a.Repository != b.Repository || a.DestinationRef != b.DestinationRef {
		return fmt.Errorf("ordinary land source changed between flows")
	}
	return nil
}

func validateDeployment(artifact Artifact, deployment Deployment, after time.Time) error {
	if deployment.UID == "" || deployment.EventID != artifact.EventID || deployment.Commit != artifact.Commit || deployment.Tree != artifact.Tree || deployment.Digest != artifact.Digest || !deployment.DeployedAt.After(after) {
		return fmt.Errorf("deployment readback differs from verified artifact %s", artifact.Commit)
	}
	return nil
}

func validateHealthReceipt(deployment Deployment, receipt HealthReceipt) error {
	if receipt.EventID != deployment.EventID || receipt.Commit != deployment.Commit || receipt.Tree != deployment.Tree || receipt.Digest != deployment.Digest || receipt.DeploymentUID != deployment.UID || !receipt.Healthy || !receipt.ObservedAt.After(deployment.DeployedAt) {
		return fmt.Errorf("health receipt differs from deployment %s", deployment.Commit)
	}
	return nil
}

func notificationRequest(kind NotificationKind, deployment Deployment, faultID string, after time.Time) NotificationRequest {
	return NotificationRequest{Kind: kind, EventID: deployment.EventID, Commit: deployment.Commit, Tree: deployment.Tree, Digest: deployment.Digest, DeploymentUID: deployment.UID, FaultID: faultID, NotBefore: after}
}

func validateNotificationReceipt(request NotificationRequest, receipt NotificationReceipt) error {
	if receipt.NotificationRequest != request || receipt.BridgeIdentity == "" || receipt.RequestID == "" || !digestPattern.MatchString(receipt.PayloadDigest) || receipt.HTTPStatus < 200 || receipt.HTTPStatus >= 300 || !receipt.DeliveredAt.After(request.NotBefore) {
		return fmt.Errorf("notification receipt differs from requested %s notification", request.Kind)
	}
	return nil
}

func validateFailureReceipt(fault Fault, receipt FailureReceipt) error {
	if receipt.FaultID != fault.ID || receipt.DeploymentUID != fault.DeploymentUID || receipt.Digest != fault.Digest || !receipt.Unhealthy || !receipt.ObservedAt.After(fault.InjectedAt) {
		return fmt.Errorf("failure receipt differs from injected fault")
	}
	return nil
}

func validateCleanupReceipt(request CleanupRequest, receipt CleanupReceipt) error {
	if receipt.FaultID != request.FaultID || receipt.EventID != request.EventID || receipt.DeploymentUID != request.DeploymentUID || receipt.Digest != request.Digest || !receipt.NoResidue || !receipt.RemovedAt.After(request.NotBefore) {
		return fmt.Errorf("cleanup receipt does not prove fault removal")
	}
	return nil
}
