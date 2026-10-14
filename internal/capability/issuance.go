package capability

import "time"

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

// Grants is the complete vocabulary. Claim grants may enter a child only through Award.
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
	{PurposePlan, UnitPlan, []Grant{ClaimsLaunch}, []Grant{
		SourceRead, SecretsRelease, DataRead, DataWrite, NodeReport, NodeResult, LogTail,
	}, []BindingKind{BindingRun}, ParentClaimantRoot},
	{PurposeEval, UnitEval, []Grant{ClaimsLaunch}, []Grant{ExpansionSubmit, DataRead}, []BindingKind{BindingNode}, ParentClaimantRoot},
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
// Root provisioning is recorded as Delegate; this reading awaits the lead's decision.
type ProvisionKind string

const (
	ProvisionBootstrap ProvisionKind = "operator_bootstrap"
	ProvisionMember    ProvisionKind = "member_token"
	ProvisionMachine   ProvisionKind = "machine_registration"
	ProvisionService   ProvisionKind = "service_registration"
	ProvisionActions   ProvisionKind = "github_actions_binding"
)

// ProvisionSpec is trusted issuer input, never decoded from a credential issuance HTTP body.
// Issue rechecks the bootstrap gate, membership, registration or verified OIDC binding in its transaction.
// The authority limits grants and pools; only operator authority sets Metered or service grants.
type ProvisionSpec struct {
	Kind           ProvisionKind
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
	panic("unimplemented: c2-credentials")
}
