package livechainacceptance

import (
	"context"
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

type ProductionSource interface {
	SelectedChain(context.Context, LandEvent) ([]Acknowledgement, error)
}

type ArtifactVerifier interface {
	VerifyArtifact(context.Context, LandEvent) (Artifact, error)
	AuthenticateArtifact(context.Context, LandEvent, Artifact) error
}

type HealthProbe interface {
	Healthy(context.Context, Deployment) (HealthReceipt, error)
	AuthenticateHealth(context.Context, Deployment, HealthReceipt) error
}

type FaultController interface {
	InjectFailure(context.Context, Deployment) (Fault, error)
	ObserveFailure(context.Context, Fault) (FailureReceipt, error)
	AuthenticateFailure(context.Context, Fault, FailureReceipt) error
	RemoveFailure(context.Context, CleanupRequest) (CleanupReceipt, error)
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
	if a.ChainLedgerID == b.ChainLedgerID {
		return fmt.Errorf("ordinary flows must use distinct event-bound production-chain ledgers")
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
