package capability

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// Owner identifies the authoritative lookup required before a route can expose a resource.
type Owner string

const (
	OwnerNone       Owner = "none"
	OwnerTeam       Owner = "team"
	OwnerRun        Owner = "run"
	OwnerAccount    Owner = "account"
	OwnerDeployment Owner = "deployment"
	OwnerNodeHost   Owner = "nodehost"
)

// BindingRule applies even to operators and local peers; there is no admin bypass.
type BindingRule string

const (
	BindNone               BindingRule = "none"
	BindOperatorSettings   BindingRule = "operator_settings"
	BindRun                BindingRule = "run"
	BindOwnNode            BindingRule = "ownNode"
	BindRunOfTrigger       BindingRule = "runOfTrigger"
	BindBodyNamesRun       BindingRule = "bodyNamesRun"
	BindPipelineOfBoundRun BindingRule = "pipelineOfBoundRun"
	BindChildOfBoundNode   BindingRule = "childOfBoundNode"
	BindSlotOfBoundNode    BindingRule = "slotOfBoundNode"
	BindReadSet            BindingRule = "readSet"
	BindDeclaredSecret     BindingRule = "declaredSecret"
	BindSource             BindingRule = "source"
	BindSourceBundle       BindingRule = "sourceBundle"
	BindData               BindingRule = "data"
	BindExpansion          BindingRule = "expansion"
	BindClaimSelection     BindingRule = "claimSelection"
	BindOwnToken           BindingRule = "ownToken"
	BindSelfOrTeamAdmin    BindingRule = "selfOrTeamAdmin"
	BindOwnCleanup         BindingRule = "ownCleanup"
	BindPlanOwner          BindingRule = "planOwner"
	BindOwnClaim           BindingRule = "ownClaim"
	BindOwnEval            BindingRule = "ownEval"
	BindSourceReport       BindingRule = "sourceReport"
)

// Liveness is checked during authentication, before grant or target refusal.
type Liveness string

const (
	LiveCredential Liveness = "credential"
	LiveSensitive  Liveness = "sensitive"
	LiveReporting  Liveness = "reporting"
	LiveResult     Liveness = "result"
	LiveLogTail    Liveness = "logTail"
	LiveHereClaim  Liveness = "hereClaim"
	LiveExpansion  Liveness = "expansion"
	LiveProcess    Liveness = "process"
)

// LivenessPolicy makes the narrow post-terminal exceptions explicit.
// Every class also requires token expiry, revocation ancestry and membership to remain valid.
type LivenessPolicy struct {
	Class               Liveness
	RefuseCancel        bool
	ResultReplayOnly    bool
	FinalLogTailOnly    bool
	CheckHereWindow     bool
	CheckExpansionLease bool
	CheckProcess        bool
}

// LivenessPolicies requires a current binding unless the named exception applies.
// Result replay checks the recorded actor, generation and digest; log tail checks its grace
// deadline (persisted attempt finish plus limits.LogSealGrace), final attempt and absence of a newer claim. Neither exception reopens execution.
var livenessPolicies = []LivenessPolicy{
	{Class: LiveCredential},
	{Class: LiveSensitive, RefuseCancel: true},
	{Class: LiveReporting},
	{Class: LiveResult, ResultReplayOnly: true},
	{Class: LiveLogTail, FinalLogTailOnly: true},
	{Class: LiveHereClaim, CheckHereWindow: true},
	{Class: LiveExpansion, RefuseCancel: true, CheckExpansionLease: true},
	{Class: LiveProcess, RefuseCancel: true, CheckProcess: true},
}

// CredentialRule constrains how authority was established, beyond a grant's spelling.
type CredentialRule string

const (
	CredentialBootstrap    CredentialRule = "pod_bootstrap"
	CredentialAny          CredentialRule = "any_live"
	CredentialMember       CredentialRule = "unbound_member_or_local"
	CredentialAccount      CredentialRule = "account_session"
	CredentialClaim        CredentialRule = "awarded_or_delegated_claim"
	CredentialEval         CredentialRule = "eval_unit"
	CredentialHere         CredentialRule = "here_award"
	CredentialLauncher     CredentialRule = "launcher_service"
	CredentialDeployment   CredentialRule = "deployment_admin"
	CredentialOperator     CredentialRule = "operator_console_session"
	CredentialNodeProcess  CredentialRule = "nodehost_process"
	CredentialOwnerProcess CredentialRule = "owner_process"
	CredentialPublisher    CredentialRule = "local_run_publisher"
	CredentialRepoReader   CredentialRule = "repo_cache_reader"
	CredentialDiscovery    CredentialRule = "discovery"
	CredentialLocalPeer    CredentialRule = "verified_local_peer"
)

// AccessRule is one complete alternative. Every grant in it is required.
// Alternatives cannot lend one another their grants, bindings or liveness exceptions.
type AccessRule struct {
	Credential CredentialRule
	Grants     []Grant
	Binding    BindingRule
	Liveness   Liveness
}

// Authentication names external proofs that do not use a Sparkwing bearer.
type Authentication string

const (
	AuthCapability     Authentication = "capability"
	AuthPublic         Authentication = "public"
	AuthGitHubWebhook  Authentication = "github_webhook_hmac"
	AuthPaymentWebhook Authentication = "payment_webhook_signature"
	AuthGitHubActions  Authentication = "github_actions_oidc"
	AuthSignedObject   Authentication = "signed_object_url"
	AuthBrowserFlow    Authentication = "browser_flow_csrf_nonce"
)

// RetryPolicy is consumed by the shared HTTP client, so retries do not grow another route table.
type RetryPolicy string

const (
	RetryNever          RetryPolicy = "never"
	RetrySafe           RetryPolicy = "safe"
	RetryIdempotencyKey RetryPolicy = "idempotency_key"
	RetryExactBody      RetryPolicy = "exact_body"
)

// RoutePolicy binds one registered method/path to all authorization metadata.
// Missing rows are policy_missing (500), including on the local controller socket.
type RoutePolicy struct {
	Method         string
	Path           string
	Audience       Audience
	Owner          Owner
	Authentication Authentication
	Access         []AccessRule
	Retry          RetryPolicy
}

// Target contains server-resolved ownership plus decoded path/body selectors.
// BodyRunID includes parent lineage on POST /runs; it cannot be authorized inside the handler.
// NodeID names the path job, or the parent job for child intake.
// Unit fields project the resolved blob.LogSubject at the controller boundary without an import cycle.
type Target struct {
	// ClaimKinds is decoded before claimSelection, which requires every kind's grant.
	ClaimKinds           []UnitKind
	SourceGeneration     int64
	Team                 string
	ClaimID              string
	Unit                 UnitKind
	UnitID               string
	UnitGeneration       int64
	UnitHolder           string
	UnitSourceNodeID     string
	UnitSourceGeneration int64
	RunID                string
	NodeID               string
	BodyRunID            string
	Pipeline             string
	ChildRunID           string
	SlotKey              string
	SlotHolderID         string
	SlotRunID            string
	SlotNodeID           string
	SlotGeneration       int64
	TokenPrefix          string
	AccountID            string
	SecretName           string
	CleanupID            string
	Data                 *DataTarget
}

// Presentation is private authentication input. Raw never belongs in process env or diagnostics.
type Presentation struct {
	Raw      string
	Class    CredentialClass
	Peer     Subject
	Evidence RequestEvidence
}

// AuthorityResolver resolves persisted capability or session authority inside the caller's transaction.
// It rechecks revocation ancestry, membership and live grants before deriving a store actor.
type AuthorityResolver interface {
	ResolveAuthority(context.Context, AuthorityRef) (Principal, error)
}

// Authenticator rechecks ancestor and binding liveness using the matched route's classes.
// Auth caches obey limits.AuthCacheTTL and compare the complete AuthGenerations on every reuse.
type Authenticator interface {
	Authenticate(context.Context, Presentation, RoutePolicy) (Authenticated, error)
}

// LiveAccess records which route alternative passed its liveness check.
// Exceptions attach to that alternative alone, so a replay cannot become a sensitive read.
type LiveAccess struct {
	RuleIndex      int
	ResultReplay   bool
	LogTailAttempt int64
}

// Authenticated binds liveness evidence to the exact policy selected by the router.
// A live credential with no matching grant still reaches Authorize and receives 403.
type Authenticated struct {
	Principal Principal
	Method    string
	Pattern   string
	Live      []LiveAccess
}

// BindingChecker supplies relational facts through the store or the nodehost's sealed plan.
// It owns lookups for team ownership, declarations, pools, ancestry, slots and data edges.
// Implementations return typed apierr errors and perform no external side effects.
// claimSelection refuses empty, duplicate, unknown or unauthorized ClaimKinds before selection.
// nodes.claim authorizes node and eval, triggers.claim authorizes trigger and plan, and
// claims.launch authorizes plan/eval/node only. The request cannot combine alternatives to widen authority.
// The named trigger route also accepts a here Award's trigger.host for its exact
// reserved run under LiveHereClaim. This exception never opens an ordered queue scan.
type BindingChecker interface {
	CheckBinding(context.Context, Principal, BindingRule, Target) error
}

// Authorized carries the server-derived fence to store writes for a transactional recheck.
// ResultReplay and LogTailAttempt permit only exact result replay or the named final log tail.
type Authorized struct {
	Result         *RecordedResult
	Principal      Principal
	Target         Target
	Binding        Binding
	Access         AccessRule
	ResultReplay   bool
	LogTailAttempt int64
}

// Authorize returns forbidden for a live principal missing a grant, audience or binding.
// Authentication failures are unauthenticated; later transaction races are lock_held or illegal_transition.
func Authorize(ctx context.Context, bindings BindingChecker, route RoutePolicy, authenticated Authenticated, target Target) (Authorized, error) {
	if validateRoutePolicy(route) != nil || bindings == nil || route.Authentication != AuthCapability ||
		authenticated.Method != route.Method || authenticated.Pattern != route.Path {
		return Authorized{}, policyFailure()
	}
	if len(authenticated.Live) == 0 {
		return Authorized{}, unauthenticated()
	}
	p := authenticated.Principal
	if p.Capability.Audience != route.Audience {
		return Authorized{}, forbidden(apierr.ReasonAudience)
	}
	var refusal error = forbidden(apierr.ReasonMissingGrant)
	for _, live := range authenticated.Live {
		if live.RuleIndex < 0 || live.RuleIndex >= len(route.Access) {
			return Authorized{}, policyFailure()
		}
		rule := route.Access[live.RuleIndex]
		if live.ResultReplay && rule.Liveness != LiveResult || live.LogTailAttempt != 0 && rule.Liveness != LiveLogTail {
			return Authorized{}, policyFailure()
		}
		if p.Capability.Delivery == DeliveryBootstrap && rule.Credential != CredentialBootstrap {
			continue
		}
		if !matchesCredential(p, rule.Credential) || !subset(rule.Grants, p.Capability.Grants) {
			continue
		}
		if err := checkOwner(p, route.Owner, rule, target); err != nil {
			refusal = err
			continue
		}
		if rule.Binding == BindClaimSelection {
			if err := claimSelection(p, route.Path, target.ClaimKinds); err != nil {
				refusal = err
				continue
			}
		}
		if !bindingShape(p, rule.Binding, target) {
			refusal = forbidden(apierr.ReasonBinding)
			continue
		}
		if err := bindings.CheckBinding(ctx, p, rule.Binding, target); err != nil {
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.Forbidden {
				return Authorized{}, err
			}
			refusal = err
			continue
		}
		return Authorized{
			Principal: p, Target: target, Binding: p.Capability.Binding, Access: rule,
			ResultReplay: live.ResultReplay, LogTailAttempt: live.LogTailAttempt,
		}, nil
	}
	return Authorized{}, refusal
}

func claimSelection(p Principal, path string, kinds []UnitKind) error {
	if path == "/api/v1/launcher/sync" {
		return nil
	}
	if len(kinds) == 0 {
		return forbidden(apierr.ReasonBinding)
	}
	seen := make(map[UnitKind]bool, len(kinds))
	for _, kind := range kinds {
		if seen[kind] {
			return forbidden(apierr.ReasonBinding)
		}
		seen[kind] = true
		var grant Grant
		switch path {
		case "/api/v1/launcher/claim":
			if kind != UnitNode && kind != UnitPlan && kind != UnitEval {
				return forbidden(apierr.ReasonBinding)
			}
			grant = ClaimsLaunch
		case "/api/v1/runners/claim", "/api/v1/runs/{run}/claim":
			switch kind {
			case UnitNode, UnitEval, UnitPlan:
				if path != "/api/v1/runners/claim" {
					return forbidden(apierr.ReasonBinding)
				}
				grant = NodesClaim
				if kind == UnitPlan {
					grant = TriggersClaim
				}
			case UnitTrigger:
				grant = TriggersClaim
			default:
				return forbidden(apierr.ReasonBinding)
			}
		default:
			return policyFailure()
		}
		if !slices.Contains(p.Capability.Grants, grant) {
			return forbidden(apierr.ReasonMissingGrant)
		}
	}
	return nil
}

func policyFailure() *apierr.Error {
	return &apierr.Error{Code: apierr.PolicyMissing, Message: "route authorization policy is incomplete"}
}

func unauthenticated() *apierr.Error {
	return &apierr.Error{Code: apierr.Unauthenticated, Message: "credential is not live"}
}

func forbidden(reason apierr.Reason) *apierr.Error {
	return apierr.Authorization(reason, "credential does not authorize this operation")
}

func subset[T comparable](child, parent []T) bool {
	for _, value := range child {
		if !slices.Contains(parent, value) {
			return false
		}
	}
	return true
}

func isClaim(p Principal) bool {
	c := p.Capability
	return p.Class == CredentialBearer && c.UnitID != "" && c.Unit != "" && c.Purpose != "" &&
		(c.Issuance == Award || c.Issuance == Exchange || c.Issuance == Delegate) && c.Binding.Kind != BindingNone && c.Binding.RunID != ""
}

func isMember(p Principal) bool {
	c := p.Capability
	if c.Binding.Kind != BindingNone || c.Unit != "" || c.Purpose != "" {
		return false
	}
	switch p.Class {
	case CredentialBearer, CredentialSession:
		return c.Subject.Kind == SubjectAccount && c.Subject.ID != ""
	case CredentialPeer:
		return c.Subject.Kind == SubjectLocal && c.Subject.ID != ""
	case CredentialAnonymous:
		return c.Subject.Kind == SubjectAnonymous
	}
	return false
}

func matchesCredential(p Principal, rule CredentialRule) bool {
	c := p.Capability
	switch rule {
	case CredentialLocalPeer:
		return p.Class == CredentialPeer && c.Subject.Kind == SubjectLocal && c.Unit == "" && c.Binding.Kind == BindingNone
	case CredentialBootstrap:
		return isClaim(p) && c.Delivery == DeliveryBootstrap
	case CredentialAny:
		return true
	case CredentialMember:
		return isMember(p)
	case CredentialAccount:
		return p.DirectAccountSession()
	case CredentialClaim:
		return isClaim(p)
	case CredentialEval:
		return isClaim(p) && c.Unit == UnitEval && c.Purpose == PurposeEval && c.Delivery != DeliveryBootstrap &&
			c.Binding.Expansion != nil && c.Binding.Expansion.Kind == UnitEval && c.Binding.Expansion.ID == c.UnitID
	case CredentialHere:
		return isClaim(p) && c.Unit == UnitTrigger && c.Purpose == PurposeHere && c.Binding.Reservation == "here:"+c.Binding.RunID
	case CredentialLauncher:
		return p.Class == CredentialBearer && Launcher(c)
	case CredentialDeployment:
		return DeploymentAuthority(p)
	case CredentialOperator:
		return p.Class == CredentialConsole && c.Subject.Kind == SubjectOperator && c.Subject.ID != "" && c.Unit == "" && c.Binding.Kind == BindingNone
	case CredentialNodeProcess:
		return p.Class == CredentialBearer && c.Audience == NodeHost && c.Binding.Kind == BindingNode && c.Binding.Holder != "" && c.Binding.Generation > 0 && c.Binding.Attempt > 0 && c.Binding.Expansion == nil
	case CredentialOwnerProcess:
		if !isClaim(p) || c.Audience != NodeHost || c.Delivery == DeliveryBootstrap {
			return false
		}
		if c.Unit == UnitTrigger {
			return (c.Purpose == PurposeTrigger || c.Purpose == PurposeHere) && c.Binding.Kind == BindingTrigger && c.Binding.NodeID == "" && c.Binding.Generation > 0 && c.Binding.Holder != "" && c.Binding.Attempt == 0 && c.Binding.Expansion == nil
		}
		if c.Unit == UnitPlan {
			return c.Purpose == PurposePlan && c.Binding.Kind == BindingRun && c.Binding.NodeID == "" && c.Binding.Generation > 0 && c.Binding.Holder != "" && c.Binding.Attempt == 0 && c.Binding.Expansion == nil
		}
		return c.Unit == UnitEval && matchesCredential(p, CredentialEval)
	case CredentialPublisher, CredentialRepoReader:
		if c.Subject.Kind != SubjectAccount || c.Unit != "" || c.Purpose != "" || c.Data == nil || (p.Class != CredentialBearer && p.Class != CredentialSession) {
			return false
		}
		if rule == CredentialPublisher {
			return c.Data.Use == DataPublish && c.Data.RunID != "" && c.Binding.Kind == BindingRun && c.Binding.RunID == c.Data.RunID
		}
		return c.Data.Use == DataRemoteCache && c.Data.RepositoryKey != "" && c.Binding.Kind == BindingNone
	case CredentialDiscovery:
		return c.Delivery != DeliveryBootstrap && c.Unit != UnitEval && c.Audience == Controller
	}
	return false
}

func checkOwner(p Principal, owner Owner, rule AccessRule, target Target) error {
	switch owner {
	case OwnerTeam, OwnerRun:
		if rule.Credential == CredentialLauncher && rule.Binding == BindClaimSelection {
			return nil
		}
		if target.Team == "" || p.Capability.Team == "" {
			return forbidden(apierr.ReasonNoTeam)
		}
		if target.Team != p.Capability.Team {
			return forbidden(apierr.ReasonTeam)
		}
	case OwnerAccount:
		if target.AccountID != "" && p.Capability.Subject.ID != target.AccountID {
			return forbidden(apierr.ReasonBinding)
		}
	case OwnerNone, OwnerDeployment, OwnerNodeHost:
	default:
		return policyFailure()
	}
	return nil
}

func bindingShape(p Principal, rule BindingRule, t Target) bool {
	b := p.Capability.Binding
	run := b.RunID != "" && b.RunID == t.RunID
	switch rule {
	case BindNone, BindOperatorSettings, BindOwnToken, BindSelfOrTeamAdmin:
		return true
	case BindRun:
		return run
	case BindOwnNode:
		return run && b.Kind == BindingNode && b.NodeID != "" && b.NodeID == t.NodeID && b.Generation > 0 && b.Holder != ""
	case BindRunOfTrigger:
		return run && b.Kind == BindingTrigger && p.Capability.Unit == UnitTrigger &&
			(b.Generation > 0 && b.Holder != "" || p.Capability.Purpose == PurposeHere && b.Reservation == "here:"+b.RunID)
	case BindBodyNamesRun:
		return isMember(p) || b.RunID != "" && t.BodyRunID == b.RunID &&
			(b.Kind != BindingNode || b.NodeID != "" && t.NodeID == b.NodeID)
	case BindSlotOfBoundNode:
		return b.RunID != "" && b.RunID == t.SlotRunID && b.Generation > 0 && b.Generation == t.SlotGeneration &&
			((b.Kind == BindingNode && b.NodeID != "" && b.NodeID == t.SlotNodeID) ||
				((b.Kind == BindingTrigger || b.Kind == BindingRun && p.Capability.Unit == UnitPlan) && t.SlotNodeID == ""))
	case BindOwnClaim:
		return isClaim(p) && t.ClaimID == p.Capability.UnitID
	case BindOwnEval:
		lease := b.Expansion
		return matchesCredential(p, CredentialEval) && run && t.ClaimID == p.Capability.UnitID &&
			t.Unit == UnitEval && t.UnitID == p.Capability.UnitID && t.UnitGeneration == lease.Generation && t.UnitHolder == lease.Holder &&
			t.UnitSourceNodeID == b.NodeID && t.UnitSourceGeneration == b.Generation
	case BindPlanOwner:
		return run && (p.Capability.Unit == UnitPlan && b.Kind == BindingRun || p.Capability.Unit == UnitTrigger && b.Kind == BindingTrigger)
	case BindSourceReport:
		return run && b.Kind == BindingNode && b.Expansion != nil && b.NodeID != "" &&
			(t.NodeID == "" || t.NodeID == b.NodeID) && b.Generation >= 0
	case BindPipelineOfBoundRun:
		return isClaim(p) && p.Capability.Delivery != DeliveryBootstrap && t.Pipeline != ""
	case BindExpansion:
		return run && b.NodeID != "" && b.NodeID == t.NodeID && b.Generation >= 0 && b.Expansion != nil
	case BindChildOfBoundNode, BindOwnCleanup:
		return b.Kind == BindingNode && b.RunID != "" && b.NodeID != "" && (t.RunID == "" || run)
	case BindDeclaredSecret, BindSource:
		return isClaim(p) && (t.RunID == "" || run)
	case BindSourceBundle:
		return matchesCredential(p, CredentialBootstrap) && t.Data != nil && t.Data.Operation == DataDownload &&
			t.Data.RunID == b.RunID && b.RunID != ""
	case BindReadSet, BindData, BindClaimSelection:
		return true
	}
	return false
}

// ValidateRoutes rejects missing, extra or duplicate registrations and malformed policy rows.
// Registration keys are METHOD followed by one space and the router pattern.
func ValidateRoutes(registered []string) error {
	policies := make(map[string]bool, len(routes))
	for _, route := range routes {
		key := route.Method + " " + route.Path
		if policies[key] {
			return fmt.Errorf("duplicate route policy: %s", key)
		}
		policies[key] = true
		if err := validateRoutePolicy(route); err != nil {
			return fmt.Errorf("route %s: %w", key, err)
		}
	}
	seen := make(map[string]bool, len(registered))
	for _, key := range registered {
		if seen[key] {
			return fmt.Errorf("duplicate registration: %s", key)
		}
		seen[key] = true
		if !policies[key] {
			return fmt.Errorf("missing route policy: %s", key)
		}
	}
	for key := range policies {
		if !seen[key] {
			return fmt.Errorf("unregistered route policy: %s", key)
		}
	}
	return nil
}

func validateRoutePolicy(route RoutePolicy) error {
	if route.Method == "" || !strings.HasPrefix(route.Path, "/") ||
		!slices.Contains([]Audience{Controller, NodeHost}, route.Audience) ||
		!slices.Contains([]Owner{OwnerNone, OwnerTeam, OwnerRun, OwnerAccount, OwnerDeployment, OwnerNodeHost}, route.Owner) ||
		!slices.Contains([]RetryPolicy{RetryNever, RetrySafe, RetryIdempotencyKey, RetryExactBody}, route.Retry) ||
		!slices.Contains([]Authentication{AuthCapability, AuthPublic, AuthGitHubWebhook, AuthPaymentWebhook, AuthGitHubActions, AuthSignedObject, AuthBrowserFlow}, route.Authentication) {
		return fmt.Errorf("invalid route metadata")
	}
	if (route.Authentication == AuthCapability) != (len(route.Access) > 0) {
		return fmt.Errorf("invalid route authentication alternatives")
	}
	for _, access := range route.Access {
		if !slices.Contains([]CredentialRule{
			CredentialBootstrap, CredentialAny, CredentialMember, CredentialAccount, CredentialClaim, CredentialEval, CredentialHere, CredentialLauncher,
			CredentialDeployment, CredentialOperator, CredentialNodeProcess, CredentialOwnerProcess, CredentialPublisher, CredentialRepoReader, CredentialDiscovery, CredentialLocalPeer,
		}, access.Credential) ||
			!slices.Contains([]BindingRule{
				BindNone, BindOperatorSettings, BindRun, BindOwnNode, BindRunOfTrigger, BindBodyNamesRun, BindPipelineOfBoundRun, BindChildOfBoundNode,
				BindSlotOfBoundNode, BindReadSet, BindDeclaredSecret, BindSource, BindSourceBundle, BindData, BindExpansion, BindClaimSelection, BindOwnToken,
				BindSelfOrTeamAdmin, BindOwnCleanup, BindPlanOwner, BindOwnClaim, BindOwnEval, BindSourceReport,
			}, access.Binding) ||
			!slices.ContainsFunc(livenessPolicies, func(policy LivenessPolicy) bool { return policy.Class == access.Liveness }) {
			return fmt.Errorf("invalid access policy")
		}
		for i, grant := range access.Grants {
			if !slices.Contains(grants, grant) || slices.Contains(access.Grants[:i], grant) {
				return fmt.Errorf("unknown or duplicate grant")
			}
		}
	}
	return nil
}
