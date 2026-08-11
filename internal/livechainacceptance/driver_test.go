package livechainacceptance

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type scriptedAcceptance struct {
	events      []LandEvent
	chains      map[string][]Acknowledgement
	log         []string
	notifyErr   NotificationKind
	removeCalls int
}

func (s *scriptedAcceptance) Next(_ context.Context) (LandEvent, error) {
	if len(s.events) == 0 {
		return LandEvent{}, fmt.Errorf("no event")
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *scriptedAcceptance) SelectedChain(_ context.Context, event LandEvent) ([]Acknowledgement, error) {
	s.log = append(s.log, "production:"+event.EventID)
	return append([]Acknowledgement(nil), s.chains[event.EventID]...), nil
}

func (s *scriptedAcceptance) VerifyArtifact(_ context.Context, event LandEvent) (Artifact, error) {
	s.log = append(s.log, "verify:"+event.EventID)
	return Artifact{EventID: event.EventID, Commit: event.Commit, Tree: event.Tree, Digest: event.ArtifactManifestDigest}, nil
}

func (s *scriptedAcceptance) Deploy(_ context.Context, artifact Artifact) (Deployment, error) {
	s.log = append(s.log, "deploy:"+artifact.Commit)
	return Deployment{EventID: artifact.EventID, Commit: artifact.Commit, Tree: artifact.Tree, Digest: artifact.Digest, UID: "deployment-" + artifact.Commit}, nil
}

func (s *scriptedAcceptance) Healthy(_ context.Context, deployment Deployment) (HealthReceipt, error) {
	s.log = append(s.log, "health:"+deployment.Commit)
	return HealthReceipt{EventID: deployment.EventID, Commit: deployment.Commit, Tree: deployment.Tree, Digest: deployment.Digest, DeploymentUID: deployment.UID, Healthy: true, ObservedAt: time.Now()}, nil
}

func (s *scriptedAcceptance) Notify(_ context.Context, req NotificationRequest) (NotificationReceipt, error) {
	s.log = append(s.log, "notify:"+string(req.Kind)+":"+req.Commit)
	if req.Kind == s.notifyErr {
		return NotificationReceipt{}, fmt.Errorf("notification failed")
	}
	return NotificationReceipt{NotificationRequest: req, BridgeIdentity: "acceptance-discord-bridge", RequestID: "request-" + string(req.Kind), PayloadDigest: testDigest, HTTPStatus: 204, DeliveredAt: time.Now()}, nil
}

func (s *scriptedAcceptance) InjectFailure(_ context.Context, deployment Deployment) (Fault, error) {
	s.log = append(s.log, "fault:"+deployment.Commit)
	return Fault{ID: "fault-b", DeploymentUID: deployment.UID, Digest: deployment.Digest}, nil
}

func (s *scriptedAcceptance) ObserveFailure(_ context.Context, fault Fault) (FailureReceipt, error) {
	s.log = append(s.log, "failure:"+fault.DeploymentUID)
	return FailureReceipt{FaultID: fault.ID, DeploymentUID: fault.DeploymentUID, Digest: fault.Digest, Unhealthy: true, ObservedAt: time.Now()}, nil
}

func (s *scriptedAcceptance) RemoveFailure(_ context.Context, fault Fault) (CleanupReceipt, error) {
	s.log = append(s.log, "remove:"+fault.ID)
	s.removeCalls++
	return CleanupReceipt{FaultID: fault.ID, NoResidue: true, RemovedAt: time.Now()}, nil
}

func (s *scriptedAcceptance) Rollback(_ context.Context, deployment Deployment) (Deployment, error) {
	s.log = append(s.log, "rollback:"+deployment.Commit)
	deployment.UID = "rollback-" + deployment.Commit
	return deployment, nil
}

func TestRunTwoOrdinaryFlowsAndRollback(t *testing.T) {
	a := validEvent()
	b := validEvent()
	b.EventID = "land-event-2"
	b.ParentCommit = a.Commit
	b.Commit = "2123456789abcdef0123456789abcdef01234567"
	b.Tree = "29abcdef0123456789abcdef0123456789abcdef"
	b.GitLedgerID = "git-ledger-2"
	b.LandRecordID = "land-record-2"
	b.LedgerPosition = a.LedgerPosition + 1
	b.LandedAt = a.LandedAt.Add(30 * time.Second)
	b.Deadline = b.LandedAt.Add(5 * time.Minute)
	script := &scriptedAcceptance{
		events: []LandEvent{a, b},
		chains: map[string][]Acknowledgement{a.EventID: validChain(t, a), b.EventID: validChain(t, b)},
	}
	proof, err := RunTwoOrdinaryFlows(context.Background(), Dependencies{
		Main: script, Authority: testAuthority{}, Production: script, Artifacts: script, Deployments: script,
		Health: script, Notifications: script, Faults: script,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Production) != 2 || proof.Production[0].Commit != a.Commit || proof.Production[1].Commit != b.Commit {
		t.Fatalf("production proof = %+v", proof.Production)
	}
	if proof.Rollback.Commit != a.Commit || proof.Rollback.UID == proof.Deployments[0].UID {
		t.Fatalf("rollback proof = %+v", proof.Rollback)
	}
	want := []string{
		"production:" + a.EventID, "verify:" + a.EventID, "deploy:" + a.Commit, "health:" + a.Commit, "notify:accepted:" + a.Commit,
		"production:" + b.EventID, "verify:" + b.EventID, "deploy:" + b.Commit, "health:" + b.Commit, "notify:accepted:" + b.Commit,
		"fault:" + b.Commit, "failure:deployment-" + b.Commit, "notify:failure:" + b.Commit, "remove:fault-b",
		"rollback:" + a.Commit, "health:" + a.Commit, "notify:rollback:" + a.Commit,
	}
	if !reflect.DeepEqual(script.log, want) {
		t.Fatalf("effects = %#v\nwant %#v", script.log, want)
	}
}

func TestRunTwoOrdinaryFlowsRejectsNonconsecutiveCommitBeforeEffects(t *testing.T) {
	a := validEvent()
	b := validEvent()
	b.EventID = "land-event-2"
	b.Commit = "2123456789abcdef0123456789abcdef01234567"
	b.ParentCommit = "3123456789abcdef0123456789abcdef01234567"
	b.GitLedgerID = "git-ledger-2"
	b.LandRecordID = "land-record-2"
	b.LedgerPosition = a.LedgerPosition + 1
	b.LandedAt = a.LandedAt.Add(time.Second)
	b.Deadline = b.LandedAt.Add(5 * time.Minute)
	script := &scriptedAcceptance{events: []LandEvent{a, b}}
	if _, err := RunTwoOrdinaryFlows(context.Background(), Dependencies{Main: script}); err == nil {
		t.Fatal("nonconsecutive main commits were accepted")
	}
	if len(script.log) != 0 {
		t.Fatalf("effects occurred before consecutive-main validation: %v", script.log)
	}
}

func TestRunTwoOrdinaryFlowsAlwaysRemovesInjectedFailure(t *testing.T) {
	a := validEvent()
	b := validEvent()
	b.EventID = "land-event-2"
	b.ParentCommit = a.Commit
	b.Commit = "2123456789abcdef0123456789abcdef01234567"
	b.Tree = "29abcdef0123456789abcdef0123456789abcdef"
	b.GitLedgerID = "git-ledger-2"
	b.LandRecordID = "land-record-2"
	b.LedgerPosition = a.LedgerPosition + 1
	b.LandedAt = a.LandedAt.Add(time.Second)
	b.Deadline = b.LandedAt.Add(5 * time.Minute)
	script := &scriptedAcceptance{
		events: []LandEvent{a, b}, notifyErr: FailureNotification,
		chains: map[string][]Acknowledgement{a.EventID: validChain(t, a), b.EventID: validChain(t, b)},
	}
	_, err := RunTwoOrdinaryFlows(context.Background(), Dependencies{
		Main: script, Authority: testAuthority{}, Production: script, Artifacts: script,
		Deployments: script, Health: script, Notifications: script, Faults: script,
	})
	if err == nil {
		t.Fatal("failure notification error was accepted")
	}
	if script.removeCalls != 1 {
		t.Fatalf("fault removal calls = %d, want 1", script.removeCalls)
	}
}
