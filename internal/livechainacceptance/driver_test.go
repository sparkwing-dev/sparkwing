package livechainacceptance

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type scriptedAcceptance struct {
	mu          sync.Mutex
	events      []LandEvent
	chains      map[string][]Acknowledgement
	log         []string
	notifyErr   NotificationKind
	removeCalls int
	clock       time.Time
}

func (s *scriptedAcceptance) tick() time.Time {
	if s.clock.IsZero() {
		s.clock = time.Date(2026, 8, 11, 13, 0, 0, 0, time.UTC)
	} else {
		s.clock = s.clock.Add(time.Second)
	}
	return s.clock
}

func (s *scriptedAcceptance) Next(_ context.Context) (LandEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return LandEvent{}, fmt.Errorf("no event")
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *scriptedAcceptance) SelectedChain(_ context.Context, event LandEvent) ([]Acknowledgement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "production:"+event.EventID)
	return append([]Acknowledgement(nil), s.chains[event.EventID]...), nil
}

func (s *scriptedAcceptance) VerifyArtifact(_ context.Context, event LandEvent) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "verify:"+event.EventID)
	return Artifact{EventID: event.EventID, Commit: event.Commit, Tree: event.Tree, Digest: event.ArtifactManifestDigest, VerifiedAt: s.tick()}, nil
}

func (s *scriptedAcceptance) Deploy(_ context.Context, artifact Artifact) (Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "deploy:"+artifact.Commit)
	return Deployment{EventID: artifact.EventID, Commit: artifact.Commit, Tree: artifact.Tree, Digest: artifact.Digest, UID: "deployment-" + artifact.Commit, DeployedAt: s.tick()}, nil
}

func (s *scriptedAcceptance) Healthy(_ context.Context, deployment Deployment) (HealthReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "health:"+deployment.Commit)
	return HealthReceipt{EventID: deployment.EventID, Commit: deployment.Commit, Tree: deployment.Tree, Digest: deployment.Digest, DeploymentUID: deployment.UID, Healthy: true, ObservedAt: s.tick()}, nil
}

func (s *scriptedAcceptance) Notify(_ context.Context, req NotificationRequest) (NotificationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "notify:"+string(req.Kind)+":"+req.Commit)
	if req.Kind == s.notifyErr {
		return NotificationReceipt{}, fmt.Errorf("notification failed")
	}
	return NotificationReceipt{NotificationRequest: req, BridgeIdentity: "acceptance-discord-bridge", RequestID: "request-" + string(req.Kind), PayloadDigest: testDigest, HTTPStatus: 204, DeliveredAt: s.tick()}, nil
}

func (s *scriptedAcceptance) InjectFailure(_ context.Context, deployment Deployment) (Fault, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "fault:"+deployment.Commit)
	return Fault{EventID: deployment.EventID, ID: "fault-b", DeploymentUID: deployment.UID, Digest: deployment.Digest, InjectedAt: s.tick()}, nil
}

func (s *scriptedAcceptance) ObserveFailure(_ context.Context, fault Fault) (FailureReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "failure:"+fault.DeploymentUID)
	return FailureReceipt{FaultID: fault.ID, DeploymentUID: fault.DeploymentUID, Digest: fault.Digest, Unhealthy: true, ObservedAt: s.tick()}, nil
}

func (s *scriptedAcceptance) RemoveFailure(_ context.Context, request CleanupRequest) (CleanupReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "remove:"+request.FaultID)
	s.removeCalls++
	return CleanupReceipt{FaultID: request.FaultID, EventID: request.EventID, DeploymentUID: request.DeploymentUID, Digest: request.Digest, NoResidue: true, RemovedAt: s.tick()}, nil
}

func (s *scriptedAcceptance) Rollback(_ context.Context, deployment Deployment) (Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "rollback:"+deployment.Commit)
	deployment.UID = "rollback-" + deployment.Commit
	deployment.DeployedAt = s.tick()
	return deployment, nil
}
