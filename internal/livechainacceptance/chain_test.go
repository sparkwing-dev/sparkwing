package livechainacceptance

import (
	"testing"
	"time"
)

const (
	testCommit = "0123456789abcdef0123456789abcdef01234567"
	testTree   = "89abcdef0123456789abcdef0123456789abcdef"
	testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func validEvent() LandEvent {
	landed := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	return LandEvent{
		EventID: "land-event-1", Repository: "sparkwing-dev/regent", DestinationRef: "refs/heads/main",
		Commit: testCommit, ParentCommit: "1123456789abcdef0123456789abcdef01234567", Tree: testTree, CertificationID: "ordinary-land-v1",
		ArtifactManifestDigest: testDigest, TrustManifestDigest: testDigest,
		SourceDigest: testDigest, GitLedgerID: "git-ledger-1", LandRecordID: "land-record-1",
		LandedAt: landed, Deadline: landed.Add(5 * time.Minute),
	}
}

func validChain(t *testing.T, event LandEvent) []Acknowledgement {
	t.Helper()
	stages := []Stage{Landed, Certified, KnownGood, Pinned, Deployed, Healthy, Discord}
	previous := event.SourceDigest
	acks := make([]Acknowledgement, len(stages))
	for i, stage := range stages {
		ack := Acknowledgement{
			Stage: stage, EventID: event.EventID, PreviousSelectedDigest: previous,
			StageEvidenceDigest: testDigest, StageAt: event.LandedAt.Add(time.Duration(i+1) * 20 * time.Second),
			Repository: event.Repository, DestinationRef: event.DestinationRef,
			Commit: event.Commit, Tree: event.Tree, CertificationID: event.CertificationID,
			ArtifactManifestDigest: event.ArtifactManifestDigest,
			TrustManifestDigest:    event.TrustManifestDigest, LandedAt: event.LandedAt, Deadline: event.Deadline,
		}
		ack.Digest = DigestAcknowledgement(ack)
		acks[i] = ack
		previous = ack.Digest
	}
	return acks
}

func TestVerifySelectedAcknowledgementChain(t *testing.T) {
	event := validEvent()
	receipt, err := VerifySelectedChain(event, validChain(t, event))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.EventID != event.EventID || receipt.TerminalStage != Discord {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.LandToSuccess != 140*time.Second {
		t.Fatalf("land-to-success = %s, want 2m20s", receipt.LandToSuccess)
	}
}

func TestVerifySelectedAcknowledgementChainRejectsEveryIdentityMutation(t *testing.T) {
	tests := map[string]func(*Acknowledgement){
		"event":           func(a *Acknowledgement) { a.EventID = "other" },
		"repository":      func(a *Acknowledgement) { a.Repository = "other/repo" },
		"destination ref": func(a *Acknowledgement) { a.DestinationRef = "refs/heads/other" },
		"commit":          func(a *Acknowledgement) { a.Commit = "1123456789abcdef0123456789abcdef01234567" },
		"tree":            func(a *Acknowledgement) { a.Tree = "99abcdef0123456789abcdef0123456789abcdef" },
		"certification":   func(a *Acknowledgement) { a.CertificationID = "other" },
		"artifact manifest": func(a *Acknowledgement) {
			a.ArtifactManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"trust manifest": func(a *Acknowledgement) {
			a.TrustManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"landed at": func(a *Acknowledgement) { a.LandedAt = a.LandedAt.Add(time.Second) },
		"deadline":  func(a *Acknowledgement) { a.Deadline = a.Deadline.Add(time.Second) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			acks := validChain(t, event)
			mutate(&acks[3])
			acks[3].Digest = DigestAcknowledgement(acks[3])
			if _, err := VerifySelectedChain(event, acks); err == nil {
				t.Fatal("mutated identity was accepted")
			}
		})
	}
}

func TestVerifySelectedAcknowledgementChainRejectsBrokenSelection(t *testing.T) {
	tests := map[string]func([]Acknowledgement){
		"missing stage":        func(a []Acknowledgement) { copy(a[2:], a[3:]); a[len(a)-1] = Acknowledgement{} },
		"wrong stage":          func(a []Acknowledgement) { a[2].Stage = Deployed },
		"digest fork":          func(a []Acknowledgement) { a[4].PreviousSelectedDigest = testDigest },
		"body digest mismatch": func(a []Acknowledgement) { a[3].Digest = testDigest },
		"late terminal": func(a []Acknowledgement) {
			a[6].StageAt = validEvent().Deadline.Add(time.Nanosecond)
			a[6].Digest = DigestAcknowledgement(a[6])
		},
		"time reversal": func(a []Acknowledgement) {
			a[4].StageAt = a[3].StageAt.Add(-time.Second)
			a[4].Digest = DigestAcknowledgement(a[4])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			acks := validChain(t, event)
			mutate(acks)
			if _, err := VerifySelectedChain(event, acks); err == nil {
				t.Fatal("broken selected chain was accepted")
			}
		})
	}
}

func TestVerifySelectedAcknowledgementChainRejectsInvalidLandDeadline(t *testing.T) {
	event := validEvent()
	event.Deadline = event.LandedAt.Add(5*time.Minute + time.Nanosecond)
	if _, err := VerifySelectedChain(event, validChain(t, event)); err == nil {
		t.Fatal("noncanonical five-minute deadline was accepted")
	}
}
