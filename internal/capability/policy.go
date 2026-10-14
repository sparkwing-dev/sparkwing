package capability

import (
	"context"
	"fmt"
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
	BindData               BindingRule = "data"
	BindExpansion          BindingRule = "expansion"
	BindClaimSelection     BindingRule = "claimSelection"
	BindOwnToken           BindingRule = "ownToken"
	BindMachineOwner       BindingRule = "machineOwner"
	BindSelfOrTeamAdmin    BindingRule = "selfOrTeamAdmin"
	BindOwnCleanup         BindingRule = "ownCleanup"
	BindPlanOwner          BindingRule = "planOwner"
	BindOwnClaim           BindingRule = "ownClaim"
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
// deadline, final attempt and absence of a newer claim. Neither exception reopens execution.
var LivenessPolicies = []LivenessPolicy{
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
	CredentialAny         CredentialRule = "any_live"
	CredentialMember      CredentialRule = "unbound_member_or_local"
	CredentialAccount     CredentialRule = "account_session"
	CredentialClaim       CredentialRule = "awarded_or_delegated_claim"
	CredentialHere        CredentialRule = "here_award"
	CredentialLauncher    CredentialRule = "launcher_service"
	CredentialDeployment  CredentialRule = "deployment_admin"
	CredentialOperator    CredentialRule = "operator_console_session"
	CredentialNodeProcess CredentialRule = "nodehost_process"
	CredentialPublisher   CredentialRule = "local_run_publisher"
	CredentialRepoReader  CredentialRule = "repo_cache_reader"
	CredentialDiscovery   CredentialRule = "discovery"
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
	AuthCapability    Authentication = "capability"
	AuthPublic        Authentication = "public"
	AuthGitHubWebhook Authentication = "github_webhook_hmac"
	AuthGitHubActions Authentication = "github_actions_oidc"
	AuthSignedObject  Authentication = "signed_object_url"
	AuthBrowserFlow   Authentication = "browser_flow_csrf_nonce"
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
type Target struct {
	Team           string
	ClaimID        string
	RunID          string
	NodeID         string
	BodyRunID      string
	Pipeline       string
	ChildRunID     string
	SlotRunID      string
	SlotNodeID     string
	SlotGeneration int64
	TokenPrefix    string
	AccountID      string
	SecretName     string
	CleanupID      string
	Data           *DataTarget
}

// Presentation is private authentication input. Raw never belongs in process env or diagnostics.
type Presentation struct {
	Raw   string
	Class CredentialClass
	Peer  Subject
}

// Authenticator rechecks ancestor and binding liveness using the matched route's classes.
// Auth caches must use both a bounded TTL and the store's revocation generation.
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
type BindingChecker interface {
	CheckBinding(context.Context, Principal, BindingRule, Target) error
}

// Authorized carries the server-derived fence to store writes for a transactional recheck.
// ResultReplay and LogTailAttempt permit only exact result replay or the named final log tail.
type Authorized struct {
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
	panic("unimplemented: c2-credentials")
}

// LookupRoute accepts the router's registered pattern, never a prefix match on untrusted paths.
func LookupRoute(method, pattern string) (RoutePolicy, bool) {
	for _, route := range Routes {
		if route.Method == method && route.Path == pattern {
			return route, true
		}
	}
	return RoutePolicy{}, false
}

// ValidateRoutes rejects missing, extra or duplicate registrations and malformed policy rows.
// Registration keys are METHOD followed by one space and the router pattern.
func ValidateRoutes(registered []string) error {
	policies := make(map[string]bool, len(Routes))
	grants := make(map[Grant]bool, len(Grants))
	for _, grant := range Grants {
		grants[grant] = true
	}
	for _, route := range Routes {
		key := route.Method + " " + route.Path
		if policies[key] {
			return fmt.Errorf("duplicate route policy: %s", key)
		}
		policies[key] = true
		if route.Audience == "" || route.Owner == "" || route.Retry == "" || route.Authentication == "" {
			return fmt.Errorf("incomplete route policy: %s", key)
		}
		if route.Authentication == AuthCapability && len(route.Access) == 0 {
			return fmt.Errorf("missing access policy: %s", key)
		}
		for _, access := range route.Access {
			if access.Credential == "" || access.Binding == "" || access.Liveness == "" {
				return fmt.Errorf("incomplete access policy: %s", key)
			}
			for _, grant := range access.Grants {
				if !grants[grant] {
					return fmt.Errorf("unknown grant %q: %s", grant, key)
				}
			}
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
