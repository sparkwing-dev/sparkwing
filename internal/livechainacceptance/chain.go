package livechainacceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

type Stage string

const (
	Landed    Stage = "LANDED"
	Certified Stage = "CERTIFIED"
	KnownGood Stage = "KNOWN_GOOD"
	Pinned    Stage = "PINNED"
	Deployed  Stage = "DEPLOYED"
	Healthy   Stage = "HEALTHY"
	Discord   Stage = "DISCORD"

	productionDeadline = 5 * time.Minute
)

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	selectedStages = [...]Stage{Landed, Certified, KnownGood, Pinned, Deployed, Healthy, Discord}
)

type LandEvent struct {
	EventID                string
	Repository             string
	DestinationRef         string
	Commit                 string
	Tree                   string
	CertificationID        string
	ArtifactManifestDigest string
	TrustManifestDigest    string
	SourceDigest           string
	LandedAt               time.Time
	Deadline               time.Time
}

type Acknowledgement struct {
	Stage                  Stage
	Digest                 string
	EventID                string
	PreviousSelectedDigest string
	StageEvidenceDigest    string
	StageAt                time.Time
	Repository             string
	DestinationRef         string
	Commit                 string
	Tree                   string
	CertificationID        string
	ArtifactManifestDigest string
	TrustManifestDigest    string
	LandedAt               time.Time
	Deadline               time.Time
}

type ProductionReceipt struct {
	EventID          string
	Commit           string
	Tree             string
	TerminalStage    Stage
	TerminalDigest   string
	SuccessAt        time.Time
	LandToSuccess    time.Duration
	Acknowledgements []Acknowledgement
}

func DigestAcknowledgement(ack Acknowledgement) string {
	ack.Digest = ""
	raw, err := json.Marshal(ack)
	if err != nil {
		panic(fmt.Sprintf("marshal acknowledgement: %v", err))
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func VerifySelectedChain(event LandEvent, acknowledgements []Acknowledgement) (ProductionReceipt, error) {
	if err := validateLandEvent(event); err != nil {
		return ProductionReceipt{}, err
	}
	if len(acknowledgements) != len(selectedStages) {
		return ProductionReceipt{}, fmt.Errorf("selected chain has %d acknowledgements, want %d", len(acknowledgements), len(selectedStages))
	}
	previousDigest := event.SourceDigest
	previousTime := event.LandedAt
	for index, ack := range acknowledgements {
		if ack.Stage != selectedStages[index] {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %d is %q, want %q", index+1, ack.Stage, selectedStages[index])
		}
		if err := validateAcknowledgementIdentity(event, ack); err != nil {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q: %w", ack.Stage, err)
		}
		if ack.PreviousSelectedDigest != previousDigest {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q does not select its predecessor", ack.Stage)
		}
		if !digestPattern.MatchString(ack.StageEvidenceDigest) {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q has invalid evidence digest", ack.Stage)
		}
		if ack.Digest != DigestAcknowledgement(ack) {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q body digest mismatch", ack.Stage)
		}
		if ack.StageAt.Before(previousTime) {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q precedes its predecessor", ack.Stage)
		}
		if ack.StageAt.After(event.Deadline) {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q missed the production deadline", ack.Stage)
		}
		previousDigest = ack.Digest
		previousTime = ack.StageAt
	}
	terminal := acknowledgements[len(acknowledgements)-1]
	return ProductionReceipt{
		EventID: event.EventID, Commit: event.Commit, Tree: event.Tree,
		TerminalStage: terminal.Stage, TerminalDigest: terminal.Digest,
		SuccessAt: terminal.StageAt, LandToSuccess: terminal.StageAt.Sub(event.LandedAt),
		Acknowledgements: append([]Acknowledgement(nil), acknowledgements...),
	}, nil
}

func validateLandEvent(event LandEvent) error {
	if event.EventID == "" || event.Repository == "" || event.DestinationRef != "refs/heads/main" || event.CertificationID == "" {
		return fmt.Errorf("land event identity is incomplete")
	}
	if !commitPattern.MatchString(event.Commit) || !commitPattern.MatchString(event.Tree) {
		return fmt.Errorf("land event Git identity is invalid")
	}
	if !digestPattern.MatchString(event.ArtifactManifestDigest) || !digestPattern.MatchString(event.TrustManifestDigest) || !digestPattern.MatchString(event.SourceDigest) {
		return fmt.Errorf("land event digest identity is invalid")
	}
	if event.LandedAt.IsZero() || !event.Deadline.Equal(event.LandedAt.Add(productionDeadline)) {
		return fmt.Errorf("land event deadline is not exactly five minutes after landing")
	}
	return nil
}

func validateAcknowledgementIdentity(event LandEvent, ack Acknowledgement) error {
	if ack.EventID != event.EventID || ack.Repository != event.Repository || ack.DestinationRef != event.DestinationRef {
		return fmt.Errorf("paired land identity changed")
	}
	if ack.Commit != event.Commit || ack.Tree != event.Tree || ack.CertificationID != event.CertificationID {
		return fmt.Errorf("commit, tree, or certification identity changed")
	}
	if ack.ArtifactManifestDigest != event.ArtifactManifestDigest || ack.TrustManifestDigest != event.TrustManifestDigest {
		return fmt.Errorf("artifact or trust manifest identity changed")
	}
	if !ack.LandedAt.Equal(event.LandedAt) || !ack.Deadline.Equal(event.Deadline) {
		return fmt.Errorf("land time or deadline changed")
	}
	return nil
}
