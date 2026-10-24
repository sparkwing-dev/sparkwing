package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// AuditActorBreakGlass identifies trusted in-pod administration.
const AuditActorBreakGlass = "sparkwing-controller admin"

type auditSystem string

const (
	auditController      auditSystem = "controller"
	auditPaymentProvider auditSystem = "payment_provider"
	auditBreakGlass      auditSystem = AuditActorBreakGlass
)

type auditActor struct {
	subject   capability.Subject
	authority capability.AuthorityRef
	holder    string
	system    auditSystem
}

func auditPrincipalActor(p capability.Principal) auditActor {
	return auditActor{subject: p.Capability.Subject, authority: p.Authority, holder: p.Capability.Binding.Holder}
}

func auditAccountActor(id string) auditActor {
	return auditActor{subject: capability.Subject{Kind: capability.SubjectAccount, ID: id}}
}

func (a auditActor) projection() (string, bool, error) {
	if a.system != "" {
		if !auditOneOf(a.system, string(auditController), string(auditPaymentProvider), string(auditBreakGlass)) || a.subject != (capability.Subject{}) || a.authority != (capability.AuthorityRef{}) || a.holder != "" {
			return "", false, fmt.Errorf("%w: invalid audit actor", ErrInvalidInput)
		}
		return string(a.system), false, nil
	}
	if a.subject.ID == "" {
		return "", false, fmt.Errorf("%w: missing audit actor", ErrInvalidInput)
	}
	if a.subject.Kind == capability.SubjectAccount {
		if strings.ContainsAny(a.subject.ID, "@\r\n\t ") {
			return "", false, fmt.Errorf("%w: invalid account actor", ErrInvalidInput)
		}
		return a.subject.ID, true, nil
	}
	if !auditOneOf(a.subject.Kind, string(capability.SubjectMachine), string(capability.SubjectService), string(capability.SubjectOperator), string(capability.SubjectLocal)) {
		return "", false, fmt.Errorf("%w: unknown audit actor", ErrInvalidInput)
	}
	identity, _ := json.Marshal([]string{string(a.subject.Kind), a.subject.ID, string(a.authority.Kind), a.authority.ID, a.holder})
	return string(a.subject.Kind) + ":" + digestAudit(string(identity)).String(), false, nil
}

// Callers supply occurrence time; only the database supplies recording time.
type auditContext struct {
	At    time.Time
	Team  Team
	Actor auditActor
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
	MaxChildrenPerRoot            int64            `json:"max_children_per_root"`
	PreviousMaxChildrenPerRoot    int64            `json:"previous_max_children_per_root"`
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
	AccountID        string      `json:"-"`
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
func (op *Operator) recordAudit(ctx context.Context, tx *storeTx, c auditContext, payload auditPayload) error {
	actor, accountActor, err := c.Actor.projection()
	if err != nil {
		return err
	}
	ev := BusinessEvent{At: c.At, Team: c.Team, Actor: actor}
	valid, repeat := true, false
	actorAllowed := c.Actor.system == ""
	switch p := payload.(type) {
	case auditAdmission:
		actorAllowed = actorAllowed || auditOneOf(c.Actor.system, string(auditController), string(auditBreakGlass))
		ev.Kind, ev.SubjectID, ev.Account = BusinessEventAccountAdmitted, p.AccountID, p.AccountID
		valid = auditOneOf(p.Via, "signup", "waitlist", "invitation")
	case auditTeamCreation:
		ev.Kind, ev.SubjectID, ev.Account = BusinessEventTeamCreated, string(c.Team), p.AccountID
	case auditFirstSuccess:
		actorAllowed = c.Actor.system == auditController
		ev.Kind, ev.RunID = businessEventFirstRunSucceeded, p.RunID
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
		actorAllowed = p.Outcome == businessEventCheckoutOpened && c.Actor.system == auditController || p.Outcome != businessEventCheckoutOpened && c.Actor.system == auditPaymentProvider
		ev.Kind, ev.SubjectID = string(p.Outcome), p.CheckoutID
		valid = auditOneOf(p.Outcome, businessEventCheckoutOpened, businessEventCheckoutFailed, businessEventCheckoutExpired, businessEventRefillFailed) && auditOneOf(p.PaymentMode, "", "test", "live")
	case auditGrant:
		valid = auditOneOf(p.ThreeDS, "", "authenticated", "attempt_acknowledged", "attempted", "failed", "not_supported", "processing", "processing_error", "unknown", "required", "unavailable", "exempted")
		ev.SubjectID = p.GrantID
		switch p.Kind {
		case CreditGrantPaid:
			actorAllowed = c.Actor.system == auditPaymentProvider
			ev.Kind = businessEventCheckoutPaid
		case creditGrantReversal:
			actorAllowed = actorAllowed || auditOneOf(c.Actor.system, string(auditPaymentProvider), string(auditBreakGlass))
			ev.Kind = businessEventCreditReversed
		case CreditGrantFree:
			actorAllowed = actorAllowed || c.Actor.system == auditBreakGlass
			ev.Kind = BusinessEventCreditGranted
		default:
			valid = false
		}
	case auditHold:
		actorAllowed = actorAllowed || auditOneOf(c.Actor.system, string(auditController), string(auditPaymentProvider), string(auditBreakGlass))
		ev.Kind, ev.SubjectID = BusinessEventTeamFrozen, p.HoldID.String()
		if p.Released {
			ev.Kind = BusinessEventTeamUnfrozen
		}
	case auditCard:
		actorAllowed = c.Actor.system == auditPaymentProvider
		ev.Kind, ev.SubjectID = string(p.Operation), p.Reference.String()
		valid = auditOneOf(p.Operation, businessEventCardAdded, businessEventCardRefused, businessEventCardBlocked, businessEventCardRefundDue)
	case auditTrust:
		actorAllowed = actorAllowed || c.Actor.system == auditBreakGlass
		ev.Kind, ev.SubjectID = BusinessEventBillingTrustChanged, p.ReceiptID
		valid = auditOneOf(p.Before, "", "granted", "revoked") && auditOneOf(p.After, "", "granted", "revoked") && validAuditStanding(p.StandingBefore) && validAuditStanding(p.StandingAfter)
	case auditStandingRequest:
		actorAllowed = actorAllowed || p.State != "open" && c.Actor.system == auditBreakGlass
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
				valid = valid && auditOneOf(tier, tierHere, tierPersonal, tierActions, tierSelfHosted, TierCloud)
			}
		}
	case auditRefillSettings:
		ev.Kind, ev.SubjectID = businessEventRefillSettings, p.ReceiptID
	case auditRefill:
		actorAllowed = c.Actor.system == auditController
		ev.Kind, ev.SubjectID = businessEventRefillOpened, p.RefillID
	case auditSecretRelease:
		ev.Kind, ev.SubjectID, ev.RunID, repeat = "secret.released", p.Name.String(), p.RunID, true
	case auditSourceRelease:
		ev.Kind, ev.SubjectID, ev.RunID, ev.Account, repeat = "git_credential.released", p.CredentialID.String(), p.RunID, p.AccountID, true
	case auditRefund:
		ev.Kind, ev.SubjectID = "operator.refund_recorded", p.ReceiptID
	default:
		valid = false
	}
	if !valid || !actorAllowed {
		return fmt.Errorf("%w: invalid audit payload or actor for kind", ErrInvalidInput)
	}
	if ev.At.IsZero() {
		ev.At = tx.now(ctx)
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
	_, err = tx.execContext(ctx, `INSERT INTO audit_events (id,ts,team,account,kind,subject_id,actor,attrs,run_id,actor_is_account,subject_is_account)
 VALUES (?,?,?,?,?,?,?,?,?,?,CASE WHEN EXISTS(SELECT 1 FROM accounts WHERE id=?) THEN 1 ELSE 0 END) ON CONFLICT DO NOTHING`, id, ev.At.UnixNano(), team, ev.Account, ev.Kind, ev.SubjectID, ev.Actor, string(attrs), ev.RunID, boolInt(accountActor), ev.SubjectID)
	return err
}

func validAuditStanding(value auditStanding) bool {
	return auditOneOf(value, "", "New", "Established", "Trusted", "High", "Held")
}

func validAuditOperation(value auditOperation) bool {
	return auditOneOf(value, "RenameAuthorizedTeam", "SetAuthorizedBuildTrust", "CreateAuthorizedInvitation", "DeleteAuthorizedInvitation", "DeleteAuthorizedTeam", "RotateCredentials", "WriteSecret", "RemoveSecret", "WriteGitCredential", "RemoveGitCredential", "ConfirmGitCredentialFor", "PutAuthorizedGitHubTrigger", "DeleteAuthorizedGitHubTrigger", "PutAuthorizedGitHubAutomation", "DeleteAuthorizedGitHubAutomation", "ProcessGitHubDeliveryAgain", "SetAuthorizedGitHubExtraRepos", "SetAuthorizedTeamSettings", "membership.removed", "membership.role:owner", "membership.role:admin", "membership.role:editor", "membership.role:reader", "runner.metered", "authority.revoked", "WriteComputeLimits", "WriteSignUpSettings", "WriteStorageSettings", "WriteTeamStorageQuota", "WriteStorageQuota", "WriteStorageAllowance", "UnbindAuthorizedGitHubInstallation", "RegisterGitHubRunner", "RevokeGitHubRunner", "githubConnectTx", "billingMutationTx")
}
