package livechainacceptance

import (
	"context"
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
	ParentCommit           string
	Tree                   string
	CertificationID        string
	ArtifactManifestDigest string
	TrustManifestDigest    string
	SourceDigest           string
	GitLedgerID            string
	LandRecordID           string
	LandLedgerID           string
	LandSequence           uint64
	ChainLedgerID          string
	ChainBasePosition      uint64
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
	AuthorityDomain        string
	SignerKeyID            string
	ImmutableVersion       string
	LedgerPosition         uint64
	Signature              string
	DiscordDelivery        *DiscordDelivery
}

type DiscordDelivery struct {
	BridgeIdentity string
	RequestID      string
	PayloadDigest  string
	HTTPStatus     int
	DeliveredAt    time.Time
}

type AuthorityReceipt struct {
	Domain           string
	LedgerID         string
	SignerKeyID      string
	ImmutableVersion string
	LedgerPosition   uint64
	VerifiedDigest   string
	InclusionDigest  string
	Signature        string
}

type AuthorityVerifier interface {
	VerifyLand(context.Context, LandEvent) (AuthorityReceipt, error)
	VerifyAcknowledgement(context.Context, LandEvent, Acknowledgement) (AuthorityReceipt, error)
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
	Authority        []AuthorityReceipt
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

func VerifySelectedChain(ctx context.Context, authority AuthorityVerifier, event LandEvent, acknowledgements []Acknowledgement) (ProductionReceipt, error) {
	if err := validateLandEvent(event); err != nil {
		return ProductionReceipt{}, err
	}
	if authority == nil {
		return ProductionReceipt{}, fmt.Errorf("production authority verifier is nil")
	}
	landAuthority, err := authority.VerifyLand(ctx, event)
	if err != nil {
		return ProductionReceipt{}, fmt.Errorf("verify land authority: %w", err)
	}
	if err := validateAuthorityReceipt(landAuthority, event.LandLedgerID, event.SourceDigest, event.LandSequence); err != nil {
		return ProductionReceipt{}, fmt.Errorf("land authority: %w", err)
	}
	if landAuthority.ImmutableVersion != event.GitLedgerID+":"+event.LandRecordID {
		return ProductionReceipt{}, fmt.Errorf("land authority immutable version does not bind both source ledgers")
	}
	if len(acknowledgements) != len(selectedStages) {
		return ProductionReceipt{}, fmt.Errorf("selected chain has %d acknowledgements, want %d", len(acknowledgements), len(selectedStages))
	}
	previousDigest := event.SourceDigest
	previousTime := event.LandedAt
	authorities := make([]AuthorityReceipt, 0, len(acknowledgements)+1)
	authorities = append(authorities, landAuthority)
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
		authorityReceipt, err := authority.VerifyAcknowledgement(ctx, event, ack)
		if err != nil {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q authority: %w", ack.Stage, err)
		}
		if err := validateAuthorityReceipt(authorityReceipt, event.ChainLedgerID, ack.Digest, event.ChainBasePosition+uint64(index)); err != nil {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q authority: %w", ack.Stage, err)
		}
		if authorityReceipt.Domain != ack.AuthorityDomain || authorityReceipt.SignerKeyID != ack.SignerKeyID || authorityReceipt.ImmutableVersion != ack.ImmutableVersion || authorityReceipt.LedgerPosition != ack.LedgerPosition || authorityReceipt.Signature != ack.Signature {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q claimed authority differs from authenticated authority", ack.Stage)
		}
		if ack.StageAt.Before(previousTime) {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q precedes its predecessor", ack.Stage)
		}
		if !ack.StageAt.Before(event.Deadline) {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q missed the production deadline", ack.Stage)
		}
		if ack.Stage == Discord {
			if err := validateDiscordDelivery(event, ack); err != nil {
				return ProductionReceipt{}, err
			}
		} else if ack.DiscordDelivery != nil {
			return ProductionReceipt{}, fmt.Errorf("selected chain stage %q carries Discord delivery evidence", ack.Stage)
		}
		previousDigest = ack.Digest
		previousTime = ack.StageAt
		authorities = append(authorities, authorityReceipt)
	}
	terminal := acknowledgements[len(acknowledgements)-1]
	return ProductionReceipt{
		EventID: event.EventID, Commit: event.Commit, Tree: event.Tree,
		TerminalStage: terminal.Stage, TerminalDigest: terminal.Digest,
		SuccessAt: terminal.StageAt, LandToSuccess: terminal.StageAt.Sub(event.LandedAt),
		Acknowledgements: append([]Acknowledgement(nil), acknowledgements...),
		Authority:        authorities,
	}, nil
}

func validateLandEvent(event LandEvent) error {
	if event.EventID == "" || event.Repository == "" || event.DestinationRef != "refs/heads/main" || event.CertificationID == "" || event.GitLedgerID == "" || event.LandRecordID == "" || event.LandLedgerID == "" || event.LandSequence == 0 || event.ChainLedgerID == "" || event.ChainBasePosition == 0 || event.LandLedgerID == event.ChainLedgerID {
		return fmt.Errorf("land event identity is incomplete")
	}
	if !commitPattern.MatchString(event.Commit) || !commitPattern.MatchString(event.ParentCommit) || !commitPattern.MatchString(event.Tree) {
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

func validateAuthorityReceipt(receipt AuthorityReceipt, ledgerID, digest string, position uint64) error {
	if receipt.Domain != "sparkwing-production-chain-v1" || receipt.LedgerID != ledgerID || receipt.SignerKeyID == "" || receipt.ImmutableVersion == "" || receipt.LedgerPosition != position || receipt.VerifiedDigest != digest || !digestPattern.MatchString(receipt.InclusionDigest) || receipt.Signature == "" {
		return fmt.Errorf("authenticated append-only ledger receipt mismatch")
	}
	return nil
}

func validateDiscordDelivery(event LandEvent, ack Acknowledgement) error {
	delivery := ack.DiscordDelivery
	if delivery == nil || delivery.BridgeIdentity == "" || delivery.RequestID == "" || !digestPattern.MatchString(delivery.PayloadDigest) || delivery.HTTPStatus < 200 || delivery.HTTPStatus >= 300 || !delivery.DeliveredAt.Equal(ack.StageAt) || !delivery.DeliveredAt.Before(event.Deadline) {
		return fmt.Errorf("terminal Discord acknowledgement lacks authenticated HTTP delivery")
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
