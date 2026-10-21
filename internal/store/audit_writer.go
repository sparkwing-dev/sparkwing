package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AuditActorBreakGlass distinguishes in-pod administration from account actions.
const AuditActorBreakGlass = "sparkwing-controller admin"

// Callers supply occurrence time; only the database supplies recording time.
type auditContext struct {
	At    time.Time
	Team  Team
	Actor string
}

type auditDigest struct{ sum [32]byte }

func digestAudit(value string) auditDigest { return auditDigest{sha256.Sum256([]byte(value))} }
func (d auditDigest) String() string       { return hex.EncodeToString(d.sum[:]) }
func (d *auditDigest) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("invalid audit digest")
	}
	copy(d.sum[:], raw)
	return nil
}
func (d auditDigest) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

type auditAdmission struct {
	AccountID string            `json:"-"`
	Via       auditAdmissionVia `json:"via"`
}
type (
	auditAdmissionVia string
	auditTeamCreation struct {
		AccountID string `json:"-"`
	}
)

type auditFirstSuccess struct {
	RunID string `json:"run_id"`
}
type auditRunDeletion struct {
	RunID string `json:"-"`
}
type auditMutation struct {
	SubjectID string         `json:"-"`
	Operation auditOperation `json:"operation"`
}
type (
	auditOperation string
	auditCheckout  struct {
		CheckoutID  string               `json:"-"`
		Outcome     auditCheckoutOutcome `json:"-"`
		AmountMicro int64                `json:"amount_micro,omitempty"`
		PaymentMode auditPaymentMode     `json:"payment_mode,omitempty"`
		EventID     auditDigest          `json:"event_id"`
	}
)

type (
	auditCheckoutOutcome string
	auditPaymentMode     string
	auditGrantKind       string
	auditThreeDS         string
	auditGrant           struct {
		GrantID        string         `json:"-"`
		Kind           auditGrantKind `json:"grant_kind"`
		AmountMicro    int64          `json:"amount_micro"`
		Reference      auditDigest    `json:"reference_digest"`
		Reverses       auditDigest    `json:"reverses_digest"`
		SessionID      auditDigest    `json:"session_digest"`
		ThreeDS        auditThreeDS   `json:"three_d_secure"`
		LiabilityShift bool           `json:"liability_shift"`
		Refill         bool           `json:"refill"`
	}
)

type auditHold struct {
	HoldID    auditDigest `json:"-"`
	Released  bool        `json:"-"`
	PaymentID auditDigest `json:"payment_digest"`
	WarningID auditDigest `json:"warning_digest"`
	NoFault   bool        `json:"no_fault"`
}
type auditCard struct {
	Reference   auditDigest        `json:"-"`
	Operation   auditCardOperation `json:"-"`
	AmountMicro int64              `json:"amount_micro,omitempty"`
}
type (
	auditCardOperation string
	auditTrust         struct {
		ReceiptID      string          `json:"-"`
		Before         auditTrustValue `json:"trust_before"`
		After          auditTrustValue `json:"trust_after"`
		StandingBefore auditStanding   `json:"standing_before"`
		StandingAfter  auditStanding   `json:"standing_after"`
	}
)

type (
	auditTrustValue      string
	auditStanding        string
	auditStandingRequest struct {
		RequestID   string            `json:"-"`
		State       auditRequestState `json:"state"`
		Tier        auditStanding     `json:"tier"`
		Standing    auditStanding     `json:"standing,omitempty"`
		HistoryTier auditStanding     `json:"history_tier,omitempty"`
	}
)

type (
	auditRequestState string
	auditInstallation struct {
		ReceiptID      string `json:"-"`
		InstallationID int64  `json:"installation_id"`
		PreviousTeam   Team   `json:"previous_team"`
		NewTeam        Team   `json:"new_team"`
	}
)

type auditTeamSettings struct {
	ReceiptID                     string           `json:"-"`
	MembersCanAddMachines         bool             `json:"members_can_add_machines"`
	PreviousMembersCanAddMachines bool             `json:"previous_members_can_add_machines"`
	ClaimRanking                  []auditClaimTier `json:"claim_ranking"`
	PreviousClaimRanking          []auditClaimTier `json:"previous_claim_ranking"`
}
type (
	auditClaimTier      string
	auditRefillSettings struct {
		ReceiptID       string `json:"-"`
		Enabled         bool   `json:"enabled"`
		ThresholdCents  int64  `json:"threshold_cents"`
		AmountCents     int64  `json:"amount_cents"`
		WeekLimitCents  int64  `json:"week_limit_cents"`
		MonthLimitCents int64  `json:"month_limit_cents"`
	}
)

type auditRefill struct {
	RefillID       string `json:"-"`
	AmountCents    int64  `json:"amount_cents"`
	BalanceMicro   int64  `json:"balance_micro"`
	ThresholdCents int64  `json:"threshold_cents"`
}
type auditSecretRelease struct {
	RunID            string      `json:"-"`
	Name             auditDigest `json:"name_digest"`
	Pipeline         auditDigest `json:"pipeline_digest"`
	NodeID           auditDigest `json:"node_digest"`
	Generation       int64       `json:"generation"`
	CapabilityPrefix auditDigest `json:"capability_digest"`
	Holder           auditDigest `json:"holder_digest"`
}
type auditSourceRelease struct {
	RunID            string      `json:"-"`
	CredentialID     auditDigest `json:"credential_digest"`
	Repository       auditDigest `json:"repository_digest"`
	NodeID           auditDigest `json:"node_digest"`
	Generation       int64       `json:"generation"`
	MachineID        auditDigest `json:"machine_digest"`
	CapabilityPrefix auditDigest `json:"capability_digest"`
}
type auditRefund struct {
	ReceiptID     string      `json:"-"`
	Digest        [32]byte    `json:"digest"`
	GrantID       string      `json:"grant_id"`
	PaidMicro     int64       `json:"paid_micro"`
	ReversedMicro int64       `json:"reversed_micro"`
	BalanceMicro  int64       `json:"balance_micro"`
	PaymentID     auditDigest `json:"payment_digest"`
	RefundID      auditDigest `json:"refund_digest"`
}

// These closed variants are the only audit inputs. Adding a kind requires a closed payload
// and its projection here; arbitrary attrs and caller prose cannot enter it.
type auditPayload interface{ isAuditPayload() }

func (auditAdmission) isAuditPayload()       {}
func (auditTeamCreation) isAuditPayload()    {}
func (auditFirstSuccess) isAuditPayload()    {}
func (auditRunDeletion) isAuditPayload()     {}
func (auditMutation) isAuditPayload()        {}
func (auditCheckout) isAuditPayload()        {}
func (auditGrant) isAuditPayload()           {}
func (auditHold) isAuditPayload()            {}
func (auditCard) isAuditPayload()            {}
func (auditTrust) isAuditPayload()           {}
func (auditStandingRequest) isAuditPayload() {}
func (auditInstallation) isAuditPayload()    {}
func (auditTeamSettings) isAuditPayload()    {}
func (auditRefillSettings) isAuditPayload()  {}
func (auditRefill) isAuditPayload()          {}
func (auditSecretRelease) isAuditPayload()   {}
func (auditSourceRelease) isAuditPayload()   {}
func (auditRefund) isAuditPayload()          {}

func auditOneOf[T ~string](value T, choices ...string) bool {
	for _, choice := range choices {
		if string(value) == choice {
			return true
		}
	}
	return false
}

// Releases, mutations and deletion authorizations retain every occurrence.
// Other facts deduplicate by team, kind and subject in the committing transaction.
func (op *Operator) recordAudit(tx *storeTx, c auditContext, payload auditPayload) error {
	ev := BusinessEvent{At: c.At, Team: c.Team, Actor: c.Actor}
	valid, repeat, systemActor := true, false, false
	switch p := payload.(type) {
	case auditAdmission:
		ev.Kind, ev.SubjectID, ev.Account = BusinessEventAccountAdmitted, p.AccountID, p.AccountID
		valid = auditOneOf(p.Via, "signup", "waitlist", "invitation")
	case auditTeamCreation:
		ev.Kind, ev.SubjectID, ev.Account = BusinessEventTeamCreated, string(c.Team), p.AccountID
	case auditFirstSuccess:
		ev.Kind, ev.RunID = BusinessEventFirstRunSucceeded, p.RunID
	case auditRunDeletion:
		ev.Kind, ev.SubjectID, ev.RunID, repeat = "run.deletion_authorized", p.RunID, p.RunID, true
	case auditMutation:
		ev.Kind, ev.SubjectID, repeat = "team.mutated", p.SubjectID, true
		valid = validAuditOperation(p.Operation)
		switch p.Operation {
		case "WriteSecret", "RemoveSecret", "WriteGitCredential", "RemoveGitCredential", "ConfirmGitCredentialFor", "RegisterGitHubRunner":
			ev.SubjectID = digestAudit(p.SubjectID).String()
		}
	case auditCheckout:
		ev.Kind, ev.SubjectID = string(p.Outcome), p.CheckoutID
		systemActor = ev.Actor == "payment_provider"
		valid = auditOneOf(p.Outcome, BusinessEventCheckoutOpened, BusinessEventCheckoutFailed, BusinessEventCheckoutExpired, BusinessEventRefillFailed) && auditOneOf(p.PaymentMode, "", "test", "live")
	case auditGrant:
		systemActor = auditOneOf(ev.Actor, "payment_provider", AuditActorBreakGlass)
		valid = auditOneOf(p.ThreeDS, "", "authenticated", "attempt_acknowledged", "attempted", "failed", "not_supported", "processing", "processing_error", "unknown", "required", "unavailable", "exempted")
		ev.SubjectID = p.GrantID
		switch p.Kind {
		case CreditGrantPaid:
			ev.Kind = BusinessEventCheckoutPaid
		case CreditGrantReversal:
			ev.Kind = BusinessEventCreditReversed
		case CreditGrantFree:
			ev.Kind = BusinessEventCreditGranted
		default:
			valid = false
		}
	case auditHold:
		ev.Kind, ev.SubjectID = BusinessEventTeamFrozen, p.HoldID.String()
		systemActor = auditOneOf(ev.Actor, "payment_provider", AuditActorBreakGlass)
		if p.Released {
			ev.Kind = BusinessEventTeamUnfrozen
		}
	case auditCard:
		ev.Kind, ev.SubjectID = string(p.Operation), p.Reference.String()
		systemActor = ev.Actor == "payment_provider"
		valid = auditOneOf(p.Operation, BusinessEventCardAdded, BusinessEventCardRefused, BusinessEventCardBlocked, BusinessEventCardRefundDue)
	case auditTrust:
		ev.Kind, ev.SubjectID = BusinessEventBillingTrustChanged, p.ReceiptID
		valid = auditOneOf(p.Before, "", "granted", "revoked") && auditOneOf(p.After, "", "granted", "revoked") && validAuditStanding(p.StandingBefore) && validAuditStanding(p.StandingAfter)
	case auditStandingRequest:
		ev.Kind, ev.SubjectID = BusinessEventStandingRequested, p.RequestID
		if p.State != "open" {
			ev.Kind = BusinessEventStandingRequestDecided
		}
		valid = auditOneOf(p.State, "open", "approved", "declined") && validAuditStanding(p.Tier) && validAuditStanding(p.Standing) && validAuditStanding(p.HistoryTier)
	case auditInstallation:
		ev.Kind, ev.SubjectID = "github.installation_reclaimed", p.ReceiptID
		valid = NormalizeTeam(p.PreviousTeam) == p.PreviousTeam && NormalizeTeam(p.NewTeam) == p.NewTeam
	case auditTeamSettings:
		ev.Kind, ev.SubjectID = "team.settings_changed", p.ReceiptID
		for _, tiers := range [][]auditClaimTier{p.ClaimRanking, p.PreviousClaimRanking} {
			for _, tier := range tiers {
				valid = valid && auditOneOf(tier, TierHere, TierPersonal, TierActions, TierSelfHosted, TierCloud)
			}
		}
	case auditRefillSettings:
		ev.Kind, ev.SubjectID = BusinessEventRefillSettings, p.ReceiptID
	case auditRefill:
		ev.Kind, ev.SubjectID = BusinessEventRefillOpened, p.RefillID
		systemActor = ev.Actor == "system"
	case auditSecretRelease:
		ev.Kind, ev.SubjectID, ev.RunID, repeat = "secret.released", p.Name.String(), p.RunID, true
	case auditSourceRelease:
		ev.Kind, ev.SubjectID, ev.RunID, repeat = "git_credential.released", p.CredentialID.String(), p.RunID, true
	case auditRefund:
		ev.Kind, ev.SubjectID = "operator.refund_recorded", p.ReceiptID
	default:
		valid = false
	}
	if !valid {
		return fmt.Errorf("%w: unknown audit enum", ErrInvalidInput)
	}
	if ev.At.IsZero() {
		ev.At = tx.now()
	}
	attrs, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	for _, value := range []string{string(ev.Team), ev.Account, ev.SubjectID, ev.RunID} {
		if strings.ContainsAny(value, "@\r\n\t ") {
			return fmt.Errorf("%w: audit identity must be an identifier", ErrInvalidInput)
		}
	}
	var id string
	if repeat {
		id, err = newCreditID("event")
		if err != nil {
			return err
		}
	} else {
		identity, _ := json.Marshal([]string{string(ev.Team), ev.Kind, ev.SubjectID})
		id = "fact:" + digestAudit(string(identity)).String()
	}
	var team any = string(ev.Team)
	if ev.Team == "" {
		team = nil
	}
	accountActor := !strings.ContainsAny(ev.Actor, "@\r\n\t ")
	_, err = tx.Exec(`INSERT INTO audit_events (id,ts,team,account,kind,subject_id,actor,attrs,run_id,actor_is_account,subject_is_account)
 VALUES (?,?,?,?,?,?,CASE WHEN (? AND EXISTS(SELECT 1 FROM accounts WHERE id=?)) OR ? THEN ? ELSE '' END,?,?,CASE WHEN ? AND EXISTS(SELECT 1 FROM accounts WHERE id=?) THEN 1 ELSE 0 END,CASE WHEN EXISTS(SELECT 1 FROM accounts WHERE id=?) THEN 1 ELSE 0 END) ON CONFLICT DO NOTHING`, id, ev.At.UnixNano(), team, ev.Account, ev.Kind, ev.SubjectID, accountActor, ev.Actor, systemActor, ev.Actor, string(attrs), ev.RunID, accountActor, ev.Actor, ev.SubjectID)
	return err
}

func validAuditStanding(value auditStanding) bool {
	return auditOneOf(value, "", "New", "Established", "Trusted", "High", "Held")
}

func validAuditOperation(value auditOperation) bool {
	return auditOneOf(value, "RenameAuthorizedTeam", "SetAuthorizedBuildTrust", "CreateAuthorizedInvitation", "DeleteAuthorizedInvitation", "DeleteAuthorizedTeam", "RotateCredentials", "WriteSecret", "RemoveSecret", "WriteGitCredential", "RemoveGitCredential", "ConfirmGitCredentialFor", "PutAuthorizedGitHubTrigger", "DeleteAuthorizedGitHubTrigger", "PutAuthorizedGitHubAutomation", "DeleteAuthorizedGitHubAutomation", "SetAuthorizedGitHubExtraRepos", "SetAuthorizedTeamSettings", "membership.removed", "membership.role:owner", "membership.role:admin", "membership.role:editor", "membership.role:reader", "runner.metered", "authority.revoked", "WriteComputeLimits", "WriteSignUpSettings", "WriteStorageSettings", "WriteTeamStorageQuota", "WriteStorageQuota", "WriteStorageAllowance", "UnbindAuthorizedGitHubInstallation", "RegisterGitHubRunner", "RevokeGitHubRunner", "githubConnectTx", "billingMutationTx")
}
