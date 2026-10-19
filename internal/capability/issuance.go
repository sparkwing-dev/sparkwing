package capability

import (
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// Grant is an exact permission name; no wildcard or admin implication exists.
type Grant string

const (
	RunsRead            Grant = "runs.read"
	RunsWrite           Grant = "runs.write"
	RunsControl         Grant = "runs.control"
	RunsState           Grant = "runs.state"
	NodesClaim          Grant = "nodes.claim"
	TriggersClaim       Grant = "triggers.claim"
	ClaimsLaunch        Grant = "claims.launch"
	LogsRead            Grant = "logs.read"
	SecretsRead         Grant = "secrets.read"
	ApprovalsWrite      Grant = "approvals.write"
	TeamAdmin           Grant = "team.admin"
	SecretsRelease      Grant = "secrets.release"
	SourceRead          Grant = "source.read"
	DataRead            Grant = "data.read"
	DataWrite           Grant = "data.write"
	OIDCMint            Grant = "oidc.mint"
	NodeReport          Grant = "node.report"
	NodeResult          Grant = "node.result"
	LogTail             Grant = "log.tail"
	RefsRead            Grant = "refs.read"
	ChildrenStart       Grant = "children.start"
	TriggerHost         Grant = "trigger.host"
	ClaimExchange       Grant = "claim.exchange"
	ExpansionSubmit     Grant = "expansion.submit"
	OperatorLimits      Grant = "operator.limits"
	OperatorObjectStore Grant = "operator.object_store"
	OperatorTokens      Grant = "operator.tokens"
	OperatorConsole     Grant = "operator.console"
)

// Grants is the complete vocabulary.
var Grants = []Grant{
	RunsRead, RunsWrite, RunsControl, RunsState, NodesClaim, TriggersClaim, ClaimsLaunch,
	LogsRead, SecretsRead, ApprovalsWrite, TeamAdmin, SecretsRelease, SourceRead,
	DataRead, DataWrite, OIDCMint, NodeReport, NodeResult, LogTail, RefsRead,
	ChildrenStart, TriggerHost, ClaimExchange, ExpansionSubmit,
	OperatorLimits, OperatorObjectStore, OperatorTokens, OperatorConsole,
}

// IssuanceKind records the rule which justified a credential, including the here Award.
type IssuanceKind string

const (
	Delegate IssuanceKind = "delegate"
	Award    IssuanceKind = "award"
	Exchange IssuanceKind = "exchange"
)

// UnitKind identifies persisted operational units independently of their grant and delivery purpose.
type UnitKind string

const (
	UnitTrigger UnitKind = "trigger"
	UnitPlan    UnitKind = "plan"
	UnitNode    UnitKind = "node"
	UnitEval    UnitKind = "eval"
)

// AwardPurpose selects a fixed grant table for an operational unit.
type AwardPurpose string

const (
	PurposeTrigger AwardPurpose = "trigger"
	PurposePlan    AwardPurpose = "plan"
	PurposeWork    AwardPurpose = "work"
	PurposeEval    AwardPurpose = "eval"
	PurposeHere    AwardPurpose = "here"
)

// DeliveryPurpose distinguishes a directly usable award from a single-use pod bootstrap.
type DeliveryPurpose string

const (
	DeliveryDirect    DeliveryPurpose = "direct"
	DeliveryBootstrap DeliveryPurpose = "bootstrap"
)

// AwardPolicy binds each grant purpose to its persisted operational kind.
// ClaimGrants is any-of; Bindings lists legal shapes derived from the awarded unit.
type AwardPolicy struct {
	Purpose     AwardPurpose
	Unit        UnitKind
	ClaimGrants []Grant
	Grants      []Grant
	Bindings    []BindingKind
	Parent      ParentRule
}

// BootstrapGrants replaces the purpose's grants until Exchange consumes the bootstrap.
var BootstrapGrants = []Grant{SourceRead, ClaimExchange}

// BootstrapUnits confines pod delivery to units accepted by the launcher.
var BootstrapUnits = []UnitKind{UnitPlan, UnitEval, UnitNode}

// ParentRule controls revocation ancestry independently of grant inheritance.
// ParentClaimantRoot stops at the nearest here Award, otherwise at the registered claimant root.
// Walking past a here Award would detach its descendants from the requester's live runs.write.
type ParentRule string

const (
	ParentDelegator       ParentRule = "delegator"
	ParentClaimantRoot    ParentRule = "claimant_root"
	ParentRequester       ParentRule = "requester"
	ParentBootstrapParent ParentRule = "bootstrap_parent"
)

// AwardPolicies is exhaustive for execution grants, including the here trigger-Award case.
// Eval receives only expansion submission and source-output read authority.
var AwardPolicies = []AwardPolicy{
	{PurposeTrigger, UnitTrigger, []Grant{TriggersClaim, TriggerHost}, []Grant{
		TriggerHost, RunsRead, RunsState, NodesClaim, NodeReport, NodeResult, LogTail,
		SecretsRelease, SourceRead, DataRead, DataWrite, OIDCMint, RefsRead, ChildrenStart, ExpansionSubmit,
	}, []BindingKind{BindingTrigger}, ParentClaimantRoot},
	{PurposeWork, UnitNode, []Grant{NodesClaim, ClaimsLaunch}, []Grant{
		NodeReport, NodeResult, LogTail, SecretsRelease, SourceRead, DataRead, DataWrite,
		OIDCMint, RefsRead, ChildrenStart,
	}, []BindingKind{BindingNode}, ParentClaimantRoot},
	{PurposePlan, UnitPlan, []Grant{TriggersClaim, ClaimsLaunch}, []Grant{
		SourceRead, SecretsRelease, DataRead, DataWrite, NodeReport, NodeResult, LogTail,
	}, []BindingKind{BindingRun}, ParentClaimantRoot},
	{PurposeEval, UnitEval, []Grant{NodesClaim, ClaimsLaunch}, []Grant{ExpansionSubmit, DataRead}, []BindingKind{BindingNode}, ParentClaimantRoot},
	{PurposeHere, UnitTrigger, []Grant{RunsWrite}, []Grant{
		TriggerHost, RunsRead, RunsState, NodesClaim, NodeReport, NodeResult, LogTail,
		SecretsRelease, SourceRead, DataRead, DataWrite, OIDCMint, RefsRead, ChildrenStart, ExpansionSubmit,
	}, []BindingKind{BindingTrigger}, ParentRequester},
}

// DelegateSpec can only narrow its parent's grants, repo/data scope, binding and lifetime.
// Controller-to-nodehost delegation keeps the same fence and is revoked on process exit.
type DelegateSpec struct {
	Parent       AuthorityRef
	Audience     Audience
	Grants       []Grant
	Binding      Binding
	Repositories []string
	Data         *DataScope
	ExpiresAt    *time.Time
}

// AwardSpec selects an already locked row; Issue derives team, fence, pools and billing from it.
// Only claims.launch may select a row in another team. PurposeHere requires UnitTrigger and HereRunID.
// DeliveryBootstrap requires launcher authority and preserves Unit, Purpose and billing through Exchange.
type AwardSpec struct {
	Claimant  AuthorityRef
	Unit      UnitKind
	UnitID    string
	HereRunID string
	Purpose   AwardPurpose
	Delivery  DeliveryPurpose
}

// ExchangeSpec consumes a bootstrap once and parents the result to the bootstrap's parent.
// The original unit kind and billing provenance survive, while the bootstrap is revoked atomically.
type ExchangeSpec struct {
	BootstrapPrefix string
}

// ProvisionKind names non-capability authority which can originate a root through Store.Issue.
// Root provisioning is the trusted root-only Delegate case, not a fourth issuance kind.
type ProvisionKind string

const (
	ProvisionBootstrap ProvisionKind = "operator_bootstrap"
	ProvisionMember    ProvisionKind = "member_token"
	ProvisionTeamAdmin ProvisionKind = "operator_team_admin"
	ProvisionMachine   ProvisionKind = "machine_registration"
	ProvisionService   ProvisionKind = "service_registration"
	ProvisionActions   ProvisionKind = "github_actions_binding"
)

// ProvisionSpec is trusted root-issuance input whose origin Store.Issue rechecks in its transaction.
type ProvisionSpec struct {
	Personal       bool
	Seed           string `json:"-"`
	Kind           ProvisionKind
	ConsoleProof   *ConsoleProof
	Authority      *AuthorityRef
	RegistrationID string
	Subject        Subject
	Team           string
	Grants         []Grant
	Binding        Binding
	ReservedPools  []string
	Repositories   []string
	Data           *DataScope
	Metered        bool
	ExpiresAt      *time.Time
}

// IssueRequest requires exactly one variant matching Kind, inside the caller's transaction.
// Provision is the root-only Delegate case and cannot be combined with DelegateSpec.
type IssueRequest struct {
	Kind      IssuanceKind
	Provision *ProvisionSpec
	Delegate  *DelegateSpec
	Award     *AwardSpec
	Exchange  *ExchangeSpec
}

// ValidateDelegation rejects any widening before persistence or in-memory nodehost admission.
func ValidateDelegation(parent Principal, child DelegateSpec, now time.Time) error {
	p := parent.Capability
	if p.RevokedAt != nil && !now.Before(*p.RevokedAt) || p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) ||
		p.Binding.Deadline != nil && !now.Before(*p.Binding.Deadline) {
		return unauthenticated()
	}
	if child.Parent.ID == "" || child.Parent != parent.Authority {
		return forbidden(apierr.ReasonBinding)
	}
	if child.Audience != Controller && child.Audience != NodeHost || p.Audience != Controller && p.Audience != child.Audience {
		return forbidden(apierr.ReasonAudience)
	}
	accountData := accountDataDelegation(parent, child)
	if !subset(child.Grants, Grants) || !subset(child.Grants, p.Grants) && !accountData {
		return forbidden(apierr.ReasonMissingGrant)
	}
	ownerExpansion := narrowOwnerExpansion(parent, child)
	dataNarrowed := narrowData(p.Data, child.Data) || accountData
	if ownerExpansion && child.Data != nil {
		dataNarrowed = narrowOwnerExpansionData(p, child)
	}
	if !RepositoryScopeNarrows(p.Repositories, child.Repositories) || !dataNarrowed {
		return forbidden(apierr.ReasonRepositoryUnauthorized)
	}
	if (!narrowBinding(p.Binding, child.Binding) && !ownerExpansion) || !boundedExpiry(child.ExpiresAt, p.ExpiresAt, now) ||
		!boundedExpiry(child.ExpiresAt, p.Binding.Deadline, now) || !boundedExpiry(child.ExpiresAt, child.Binding.Deadline, now) {
		return forbidden(apierr.ReasonBinding)
	}
	return nil
}

func accountDataDelegation(parent Principal, child DelegateSpec) bool {
	p, d := parent.Capability, child.Data
	if p.Subject.Kind != SubjectAccount || p.Audience != Controller || p.Unit != "" || p.Data != nil ||
		p.Binding != (Binding{Kind: BindingNone}) || d == nil || d.RepositoryKey == "" || child.Audience != Controller ||
		!slices.Equal(child.Repositories, []string{d.RepositoryKey}) || !RepositoryScopeNarrows(p.Repositories, child.Repositories) || d.NodeID != "" || d.Attempt != 0 || d.Generation != 0 {
		return false
	}
	switch d.Use {
	case DataRemoteCache:
		// safety: dependency-cache archives are not mask-checked, so remote cache reads need the role that runs code.
		return slices.Contains(p.Grants, RunsWrite) && slices.Equal(child.Grants, []Grant{DataRead}) &&
			child.Binding == (Binding{Kind: BindingNone}) && d.VerifiedDefaultOnly && d.WriteRef == "" && d.RunID == "" && len(d.ReadRefs) > 0
	case DataPublish:
		return slices.Contains(p.Grants, RunsWrite) && slices.Equal(child.Grants, []Grant{DataWrite}) &&
			d.RunID != "" && child.Binding == (Binding{Kind: BindingRun, RunID: d.RunID}) && !d.VerifiedDefaultOnly &&
			d.WriteRef != "" && slices.Equal(d.ReadRefs, []string{d.WriteRef})
	}
	return false
}

// InheritedGrants intersects ordinary delegation and rechecks scoped account data opt-ins.
func InheritedGrants(parent Principal, child Capability) []Grant {
	if accountDataDelegation(parent, DelegateSpec{Audience: child.Audience, Grants: child.Grants, Binding: child.Binding, Repositories: child.Repositories, Data: child.Data}) {
		return slices.Clone(child.Grants)
	}
	return slices.DeleteFunc(slices.Clone(child.Grants), func(g Grant) bool { return !slices.Contains(parent.Capability.Grants, g) })
}

func boundedExpiry(child, parent *time.Time, now time.Time) bool {
	return (child == nil || now.Before(*child)) && (parent == nil || child != nil && !child.After(*parent))
}

func narrowBinding(parent, child Binding) bool {
	if !slices.Contains([]BindingKind{BindingNone, BindingRun, BindingNode, BindingTrigger, BindingPush}, child.Kind) {
		return false
	}
	if parent.Kind != BindingNone && child.Kind != parent.Kind {
		if parent.Kind != BindingRun || child.Kind != BindingNode {
			return false
		}
	}
	if (parent.Generation != 0 || parent.Expansion != nil) && child.Generation != parent.Generation ||
		(parent.Attempt != 0 || parent.Expansion != nil) && child.Attempt != parent.Attempt ||
		parent.Holder != "" && child.Holder != parent.Holder {
		return false
	}
	if parent.Expansion == nil && child.Expansion != nil || parent.Expansion != nil && (child.Expansion == nil || *child.Expansion != *parent.Expansion) {
		return false
	}
	return narrowBindingScope(parent, child)
}

func narrowBindingScope(parent, child Binding) bool {
	if parent.RunID != "" && child.RunID != parent.RunID || parent.NodeID != "" && child.NodeID != parent.NodeID ||
		parent.Reservation != "" && child.Reservation != parent.Reservation {
		return false
	}
	if parent.ClaimBy != nil && (child.ClaimBy == nil || child.ClaimBy.After(*parent.ClaimBy)) ||
		parent.Deadline != nil && (child.Deadline == nil || child.Deadline.After(*parent.Deadline)) {
		return false
	}
	if parent.Push != nil && (child.Push == nil || *child.Push != *parent.Push) {
		return false
	}
	return true
}

// safety: a run owner may narrow to a terminal source while retaining its own
// lease as expansion authority. The source generation is a separate fence.
func narrowOwnerExpansion(parent Principal, child DelegateSpec) bool {
	p, b, source := parent.Capability, parent.Capability.Binding, child.Binding
	if !isClaim(parent) || p.Unit != UnitTrigger || p.UnitID == "" || b.Kind != BindingTrigger || b.NodeID != "" ||
		(p.Purpose != PurposeTrigger && p.Purpose != PurposeHere) || b.Expansion != nil || b.Generation <= 0 || b.Holder == "" ||
		!slices.Contains(p.Grants, ExpansionSubmit) || child.Audience != Controller ||
		!subset(child.Grants, []Grant{ExpansionSubmit, DataRead}) || source.Kind != BindingNode || source.NodeID == "" || source.Generation < 0 || source.Attempt < 0 || source.Expansion == nil {
		return false
	}
	owner := LeaseRef{Kind: UnitTrigger, ID: p.UnitID, Generation: b.Generation, Holder: b.Holder}
	return *source.Expansion == owner && narrowBindingScope(b, source)
}

func narrowOwnerExpansionData(parent Capability, child DelegateSpec) bool {
	p, c := parent.Data, child.Data
	if p == nil || c == nil || p.Use != DataClaim || c.Use != DataExpansion || p.RunID != parent.Binding.RunID ||
		p.NodeID != "" || p.Attempt != 0 || p.Generation != 0 && p.Generation != parent.Binding.Generation || c.WriteRef != "" ||
		c.Generation != child.Binding.Generation || c.Attempt != child.Binding.Attempt {
		return false
	}
	source := *p
	source.Use, source.NodeID = DataExpansion, child.Binding.NodeID
	source.Generation, source.Attempt = child.Binding.Generation, child.Binding.Attempt
	return narrowData(&source, c)
}

func narrowData(parent, child *DataScope) bool {
	if child == nil {
		return true
	}
	if parent == nil || parent.Use != child.Use || parent.RepositoryKey == "" || child.RepositoryKey != parent.RepositoryKey {
		return false
	}
	return (child.WriteRef == "" || child.WriteRef == parent.WriteRef) && subset(child.ReadRefs, parent.ReadRefs) &&
		(!parent.VerifiedDefaultOnly || child.VerifiedDefaultOnly) &&
		(parent.RunID == "" || child.RunID == parent.RunID) && (parent.NodeID == "" || child.NodeID == parent.NodeID) &&
		(parent.Attempt == 0 && parent.Use != DataExpansion || child.Attempt == parent.Attempt) &&
		(parent.Generation == 0 && parent.Use != DataExpansion || child.Generation == parent.Generation)
}

// ValidateProvisionSeed confines supplied presentations to the bootstrap origin.
func ValidateProvisionSeed(spec ProvisionSpec) error {
	if spec.Seed == "" {
		return nil
	}
	lookup, err := credentialLookup(Presentation{Class: CredentialBearer, Raw: spec.Seed})
	if err != nil || lookup.Audience != Controller || spec.Kind != ProvisionBootstrap {
		return &apierr.Error{Code: apierr.BadRequest, Message: "invalid bootstrap credential"}
	}
	return nil
}
