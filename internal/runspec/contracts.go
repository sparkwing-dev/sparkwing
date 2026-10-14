// safety: Package runspec carries immutable run inputs across both seal points.
// Credential authorization, setting resolution and plan validation belong to their owners.
package runspec

import (
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// safety: Optional preserves an explicitly selected zero separately from absence.
type Optional[T any] struct {
	Set   bool `json:"set"`
	Value T    `json:"value"`
}

// safety: Hash is a lowercase SHA-256 of canonical, versioned JSON.
type Hash string

// safety: RepoKey identifies a repository without conferring authority.
type RepoKey string

// safety: AttemptIdentity uses a node-wide positive Attempt that never resets.
// Generation is a fence attribute, not the attempt ordinal or its identity.
type AttemptIdentity struct {
	Team       string `json:"team"`
	RunID      string `json:"run_id"`
	NodeID     string `json:"job_id"`
	Attempt    int64  `json:"attempt"`
	Generation int64  `json:"generation"`
}

// safety: RepoInput supplies independently resolved identity facts, never authorization.
type RepoInput struct {
	GitHubRepositoryID Optional[int64]
	RemoteURL          string
	AbsolutePath       string
	Display            string
}

// safety: Repository retains a human spelling beside the canonical key.
type Repository struct {
	Key     RepoKey `json:"key"`
	Display string  `json:"display"`
}

// safety: RepoRule makes case and machine-scope rules available to conformance tests.
type RepoRule struct {
	Kind         string
	Prefix       string
	HostCase     string
	PathCase     string
	MachineLocal bool
}

// safety: RepoRules enumerates every canonical identity form.
var RepoRules = []RepoRule{
	{"github", "gh:", "lower", "numeric_id", false},
	{"git", "git:", "lower", "preserve_except_provider_table", false},
	{"filesystem", "local:", "", "sha256_clean_absolute_path", true},
}

// safety: CaseInsensitiveProviders is the complete approved path-folding set.
var CaseInsensitiveProviders = []string{"gitlab.com", "bitbucket.org"}

// safety: CanonicalRepository strips URL credentials and trailing .git, preserves ports,
// and hashes cleaned absolute filesystem paths. Resolving a GitHub ID grants no access.
func CanonicalRepository(RepoInput) (Repository, error) { panic("unimplemented: c1-data") }

// safety: RepoUse separates machine-local storage from controller authorization scopes.
type RepoUse string

const (
	RepoMachineStore   RepoUse = "machine_store"
	RepoAccessRule     RepoUse = "access_rule"
	RepoControllerMemo RepoUse = "controller_memo"
)

// safety: ValidateRepoUse refuses local keys in controller access rules and memo writes.
func ValidateRepoUse(RepoKey, RepoUse) error { panic("unimplemented: c1-data") }

// safety: IntakeMode prevents published history from obtaining executable owner claims.
type IntakeMode string

const (
	IntakeExecute IntakeMode = "execute"
	IntakePublish IntakeMode = "publish"
)

// safety: Dispatch selects the owner of scheduling after acceptance.
type Dispatch string

const (
	DispatchHost       Dispatch = "host"
	DispatchController Dispatch = "controller"
)

// safety: Origin identifies the intake path without making it a trust assertion.
type Origin string

const (
	OriginLocal    Origin = "local"
	OriginTrigger  Origin = "trigger"
	OriginApp      Origin = "github_app"
	OriginSchedule Origin = "schedule"
	OriginChild    Origin = "child"
	OriginRetry    Origin = "retry"
	OriginActions  Origin = "github_actions"
	OriginPublish  Origin = "publish"
)

// safety: Actor preserves the original actor when a different person retries a run.
type Actor struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Display string `json:"display"`
}

// safety: PullRequest keeps base and head facts so intake can refuse forks before award.
type PullRequest struct {
	Number   int64   `json:"number"`
	BaseRepo RepoKey `json:"base_repo"`
	HeadRepo RepoKey `json:"head_repo"`
	BaseRef  string  `json:"base_ref"`
	HeadRef  string  `json:"head_ref"`
	HeadSHA  string  `json:"head_sha"`
	Fork     bool    `json:"fork"`
}

// safety: AppFacts is server-populated only after HMAC verification and replay-key dedup.
// No current-head lookup changes these facts, including on a retry of an older SHA.
type AppFacts struct {
	DeliveryKey       string    `json:"delivery_key"`
	InstallationID    int64     `json:"installation_id"`
	RepositoryID      int64     `json:"repository_id"`
	Ref               string    `json:"ref"`
	SHA               string    `json:"sha"`
	DefaultBranch     string    `json:"default_branch"`
	DefaultBranchPush bool      `json:"default_branch_push"`
	VerifiedAt        time.Time `json:"verified_at"`
}

// safety: Git pins checkout to the sealed SHA, never the later head of Ref.
type Git struct {
	Repository    Repository         `json:"repository"`
	Ref           string             `json:"ref"`
	SHA           string             `json:"sha"`
	DefaultBranch Optional[string]   `json:"default_branch"`
	App           Optional[AppFacts] `json:"app"`
}

// safety: EnvSnapshot persists only names and a digest; values remain in private host storage.
type EnvSnapshot struct {
	Names  []string `json:"names"`
	Digest Hash     `json:"digest"`
}

// safety: Source records acquisition intent without embedding source credentials.
type Source struct {
	Ref          string           `json:"ref"`
	Entrypoint   string           `json:"entrypoint"`
	BundleDigest Optional[string] `json:"bundle_digest"`
}

// safety: Lineage records retriers separately from immutable original actor and git facts.
type Lineage struct {
	ParentRunID string          `json:"parent_run_id"`
	ParentJobID string          `json:"parent_job_id"`
	RetryOf     string          `json:"retry_of"`
	Retrier     Optional[Actor] `json:"retrier"`
}

// safety: Layer names the source to report when an explicit setting is invalid.
type Layer string

const (
	LayerFlag            Layer = "flag"
	LayerEnv             Layer = "env"
	LayerSealed          Layer = "sealed_spec"
	LayerProjectPipeline Layer = "project_pipeline"
	LayerProjectDefaults Layer = "project_defaults"
	LayerCode            Layer = "pipeline_code"
	LayerMachine         Layer = "machine"
	LayerDefault         Layer = "default"
	LayerOperatorTeam    Layer = "operator.team"
	LayerOperatorGlobal  Layer = "operator.global"
	LayerOperatorProfile Layer = "operator.profile"
)

// safety: SettingOrigin allows C4 errors to name source, file, key and line.
type SettingOrigin struct {
	Layer Layer  `json:"layer"`
	File  string `json:"file"`
	Key   string `json:"key"`
	Line  int    `json:"line,omitempty"`
}

// safety: Setting retains presence and origin without resolving precedence in this package.
type Setting[T any] struct {
	Optional[T]
	Origin SettingOrigin `json:"origin"`
}

// safety: Resources preserves C4 floating cores losslessly; platform conversion happens at dispatch.
// Cloud scratch is fixed by limits and is never an author setting.
type Resources struct {
	Cores       Setting[float64] `json:"cores"`
	MemoryBytes Setting[int64]   `json:"memory_bytes"`
}

// PauseSelection keeps each pause control's presence and origin at intake.
type PauseSelection struct {
	Before    Setting[[]string] `json:"before"`
	After     Setting[[]string] `json:"after"`
	OnFailure Setting[bool]     `json:"on_failure"`
}

// CheckoutPolicy preserves the C3 policy and each field's winning origin.
// Origins uses full_history, depth, tags, submodules and lfs; every present field has one.
type CheckoutPolicy struct {
	Policy  nodeproto.SourceSpec     `json:"policy"`
	Origins map[string]SettingOrigin `json:"origins"`
}

// JobExecutionSettings is frozen independently of the declaration used for code reconstruction.
// An unset timeout means no job-specific bound; owner deadlines remain independent.
type JobExecutionSettings struct {
	Timeout           Setting[time.Duration] `json:"timeout"`
	NoProgressTimeout Setting[time.Duration] `json:"no_progress_timeout"`
	ApprovalWait      Setting[time.Duration] `json:"approval_wait"`
	Retry             Setting[int]           `json:"retry"`
	RetryBackoff      Setting[time.Duration] `json:"retry_backoff"`
	MemoTTL           Setting[time.Duration] `json:"memo_ttl"`
	Resources         Setting[Resources]     `json:"resources"`
	Requires          Setting[[]string]      `json:"requires"`
	Prefers           Setting[[]string]      `json:"prefers"`
}

// GuardDeclarations contains author predicates before evaluation adds a verdict.
type GuardDeclarations struct {
	When   []string `json:"when"`
	Unless []string `json:"unless"`
}

// CheckoutLayer contains only author settings, never startup controls or credentials.
// C4's source table still decides which fields each layer may set. Unsupported fields
// are refused rather than promoted to a higher-precedence source through their Origin.
type CheckoutLayer struct {
	Args           Setting[map[string]Setting[string]] `json:"args"`
	Guards         Setting[GuardDeclarations]          `json:"guards"`
	Requires       Setting[[]string]                   `json:"requires"`
	Prefers        Setting[[]string]                   `json:"prefers"`
	Profile        Setting[string]                     `json:"profile"`
	Resources      Setting[Resources]                  `json:"resources"`
	Priority       Setting[int]                        `json:"priority"`
	ClaimWait      Setting[time.Duration]              `json:"claim_wait"`
	AdmissionClass Setting[string]                     `json:"admission_class"`
	Checkout       Setting[CheckoutPolicy]             `json:"checkout"`
	JobDefaults    JobExecutionSettings                `json:"job_defaults"`
}

// CheckoutDeclarations preserves both checkout layers before any precedence merge.
// Origins are diagnostic evidence, not source authority. Pipeline and Defaults assign
// their source structurally. File is checkout-relative; absence keeps both layers unset.
type CheckoutDeclarations struct {
	Version  int           `json:"version"`
	File     string        `json:"file"`
	Pipeline CheckoutLayer `json:"pipeline"`
	Defaults CheckoutLayer `json:"defaults"`
}

// CheckoutDeclarationsVersion identifies the restricted declaration format in both transports and seals.
const CheckoutDeclarationsVersion = 1

// RunnerSelection records the requested route without granting runner identity or pool access.
// Kind is any, name, label or here; Value is present only for name and label.
type RunnerSelection struct {
	Kind  RunnerSelectionKind `json:"kind"`
	Value string              `json:"value"`
}

// RunnerSelectionKind preserves here as an explicit intake request for C2's run-bound award.
type RunnerSelectionKind string

const (
	RunnerAny   RunnerSelectionKind = "any"
	RunnerName  RunnerSelectionKind = "name"
	RunnerLabel RunnerSelectionKind = "label"
	RunnerHere  RunnerSelectionKind = "here"
)

// safety: Options carries request winners and resolved values without zero-as-default rules.
type Options struct {
	Runner                Setting[RunnerSelection] `json:"runner"`
	Resources             Setting[Resources]       `json:"resources"`
	Priority              Setting[int]             `json:"priority"`
	AdmissionClass        Setting[string]          `json:"admission_class"`
	ClaimWait             Setting[time.Duration]   `json:"claim_wait"`
	OwnerExecutionTimeout Setting[time.Duration]   `json:"owner_execution_timeout"`
	Checkout              Setting[CheckoutPolicy]  `json:"checkout"`
	Requires              Setting[[]string]        `json:"requires"`
	Prefers               Setting[[]string]        `json:"prefers"`
	DryRun                Setting[bool]            `json:"dry_run"`
	NoMemo                Setting[bool]            `json:"no_memo"`
	AllowRisk             Setting[[]string]        `json:"allow_risk"`
	Workers               Setting[int]             `json:"workers"`
	Output                Setting[string]          `json:"output"`
	Pause                 Setting[PauseSelection]  `json:"pause"`
	PauseTimeout          Setting[time.Duration]   `json:"pause_timeout"`
	Debug                 Setting[bool]            `json:"debug"`
	Publish               Setting[string]          `json:"publish"`
	CacheFrom             Setting[string]          `json:"cache_from"`
	SubmitEnvAllow        Setting[[]string]        `json:"submit_env_allow"`
	RepoLocation          Setting[string]          `json:"repo_location"`
	Profile               Setting[string]          `json:"profile"`
	JobDefaults           JobExecutionSettings     `json:"job_defaults"`
}

// safety: Ceilings snapshots operator bounds for C4 to enforce without silent clamping.
type Ceilings struct {
	Resources               Setting[Resources]              `json:"resources"`
	Priority                Setting[int]                    `json:"priority"`
	OwnerExecutionTimeout   Setting[time.Duration]          `json:"owner_execution_timeout"`
	ClaimWait               Setting[time.Duration]          `json:"claim_wait"`
	Retry                   Setting[int]                    `json:"retry"`
	MemoTTL                 Setting[time.Duration]          `json:"memo_ttl"`
	AllowedAdmissionClasses Setting[[]string]               `json:"allowed_admission_classes"`
	AllowedSources          Setting[[]nodeproto.SourceSpec] `json:"allowed_sources"`
}

// ExecutionSettings contains one sealed winner set for each declared job ID.
// Expansions store additional job settings on their node rows without changing this seal.
type ExecutionSettings struct {
	Options Options                         `json:"options"`
	Jobs    map[string]JobExecutionSettings `json:"jobs"`
}

// safety: Request is submitted to the one intake transaction on every placement.
// App facts, allowed pools and authority references are derived by intake, not trusted from JSON.
// Raw requested argument values are restricted execution inputs, not a display document.
// SecretArgs is unset before describe. Display must then redact every requested value.
type Request struct {
	Version           int                   `json:"version"`
	Team              string                `json:"team"`
	Pipeline          string                `json:"pipeline"`
	Mode              IntakeMode            `json:"mode"`
	Origin            Origin                `json:"origin"`
	Dispatch          Dispatch              `json:"dispatch"`
	SecretArgs        Optional[[]string]    `json:"secret_args"`
	RequestedArgs     map[string]string     `json:"requested_args"`
	Git               Git                   `json:"git"`
	Actor             Actor                 `json:"actor"`
	PullRequest       Optional[PullRequest] `json:"pull_request"`
	Source            Source                `json:"source"`
	Lineage           Lineage               `json:"lineage"`
	Env               EnvSnapshot           `json:"env"`
	Options           Options               `json:"options"`
	ExecutionLocation string                `json:"execution_location"`
}

// safety: IntakeFacts are server-derived, with opaque authority IDs resolved only by C2.
type IntakeFacts struct {
	RunID        string
	AdmittedAt   time.Time
	AllowedPools []string
	AuthorityRef capability.AuthorityRef
	Ceilings     Ceilings
}

// safety: SealedRequest is immutable at intake, even when later settings resolve from code.
type SealedRequest struct {
	Version      int                     `json:"version"`
	Request      Request                 `json:"request"`
	RunID        string                  `json:"run_id"`
	AdmittedAt   time.Time               `json:"admitted_at"`
	AllowedPools []string                `json:"allowed_pools"`
	AuthorityRef capability.AuthorityRef `json:"authority_ref"`
	Ceilings     Ceilings                `json:"ceilings"`
	Hash         Hash                    `json:"hash"`
}

// safety: SealRequest canonicalizes immutable intake data after the intake owner authorizes it.
func SealRequest(Request, IntakeFacts) (SealedRequest, error) { panic("unimplemented: c1-data") }

// safety: DefaultBranchSource limits discovery to App facts or the acquired origin HEAD.
type DefaultBranchSource string

const (
	DefaultBranchApp        DefaultBranchSource = "app"
	DefaultBranchOriginHEAD DefaultBranchSource = "origin_head"
)

// safety: Guards preserves refusal evidence so a rejected run seals the same inputs as a plan.
type Guards struct {
	When   []string `json:"when"`
	Unless []string `json:"unless"`
	Passed bool     `json:"passed"`
	Reason string   `json:"reason"`
}

// safety: Resolved contains checkout-derived facts and C4-resolved settings for seal point two.
// SecretArgs must be present (an empty, non-nil list means none) after describe.
// Raw Args remain available for execution, retries and masking, never direct display.
type Resolved struct {
	// Engine pins the portable verified release identity; executors resolve their own platform asset.
	Engine              engine.Identity     `json:"engine"`
	Args                map[string]string   `json:"args"`
	SecretArgs          []string            `json:"secret_args"`
	SecretNames         []string            `json:"secret_names"`
	Entrypoint          string              `json:"entrypoint"`
	DefaultBranch       Optional[string]    `json:"default_branch"`
	DefaultBranchSource DefaultBranchSource `json:"default_branch_source"`
	Guards              Guards              `json:"guards"`
	Settings            ExecutionSettings   `json:"settings"`
	Env                 EnvSnapshot         `json:"env"`
}

// safety: Spec binds both seals and the accepted plan hash as the one host input.
type Spec struct {
	Declarations CheckoutDeclarations `json:"declarations"`
	Version      int                  `json:"version"`
	Request      SealedRequest        `json:"request"`
	Resolved     Resolved             `json:"resolved"`
	PlanHash     Hash                 `json:"plan_hash"`
	Hash         Hash                 `json:"hash"`
	SealedAt     time.Time            `json:"sealed_at"`
}

// safety: SealInput accepts raw plan bytes already validated by C3's ValidatePlan.
// The store performs that validation and C4 ceiling checks in its acceptance transaction.
// Declarations is the exact author-only projection used to recompute Resolved, not submitted winners.
type SealInput struct {
	Declarations CheckoutDeclarations
	Request      SealedRequest
	Resolved     Resolved
	Plan         json.RawMessage
	Now          time.Time
}

// safety: SealSpec binds declarations, resolved winners and the canonical plan without owning validation.
// The declaration layers participate in the seal hash and survive retry unchanged.
func SealSpec(SealInput) (Spec, error) { panic("unimplemented: c1-data") }

// safety: CheckSeal permits an identical replay and refuses a different hash as spec_drift.
func CheckSeal(existing, proposed Spec) (replayed bool, err error) { panic("unimplemented: c1-data") }

// safety: Projections supplies write-once indexed fields derived only from the sealed spec.
type Projections struct {
	RepoKey       RepoKey
	RepoDisplay   string
	Pipeline      string
	SHA           string
	Ref           string
	DefaultBranch string
	Dispatch      Dispatch
	AllowedPools  []string
	RequestHash   Hash
	SpecHash      Hash
	PlanHash      Hash
}

// safety: Project prevents independently writable copies of sealed run inputs.
func Project(Spec) Projections { panic("unimplemented: c1-data") }

// safety: RetryInput carries sealed state unchanged and records the new requester as lineage.
type RetryInput struct {
	Request     SealedRequest
	Spec        Optional[Spec]
	Plan        json.RawMessage
	Retrier     Actor
	SourceRunID string
}

// safety: RetrySnapshot preserves source trust and seals while adding separate retry lineage.
type RetrySnapshot struct {
	Request SealedRequest
	Spec    Optional[Spec]
	Plan    json.RawMessage
	Lineage Lineage
}

// safety: CarryRetry does not consult branch head or reconstruct inputs from current settings.
func CarryRetry(RetryInput) (RetrySnapshot, error) { panic("unimplemented: c1-data") }

// safety: Context provides trusted execution inputs on every placement.
// It may contain secret argument values and must not be rendered or logged.
type Context struct {
	Args        map[string]string
	Git         Git
	Actor       Actor
	PullRequest Optional[PullRequest]
	Source      Source
	Lineage     Lineage
	DryRun      bool
}

// safety: RunContext refuses unsealed input through the Spec-only method boundary.
func (Spec) RunContext() Context { panic("unimplemented: c1-data") }

// safety: GuardContext retains trusted default-branch provenance for fail-closed guards.
func (Spec) GuardContext() Context { panic("unimplemented: c1-data") }

// safety: Reproducer excludes secret argument values and all credentials.
func (Spec) Reproducer() []string { panic("unimplemented: c1-data") }

// safety: CanonicalHash excludes the hash field itself and nonsemantic seal timestamps.
// Sorted object keys, stable list ordering and explicit Optional presence prevent hash drift.
func CanonicalHash(value any) (Hash, error) { panic("unimplemented: c1-data") }

// safety: EnvironmentDigest returns only sorted names and a digest of their captured values.
// Callers retain values in private host storage and never persist them in the spec.
func EnvironmentDigest(values map[string]string) (EnvSnapshot, error) {
	panic("unimplemented: c1-data")
}

// safety: RedactedArgValue matches the masker so displays never expose secret argument values.
const RedactedArgValue = "***"

// safety: Display is a separate safe projection, because serializing Spec exposes execution inputs.
// Hashes and env digests are omitted because deterministic hashes can reveal guessed secret values.
type Display struct {
	Pipeline      string                `json:"pipeline"`
	RequestedArgs map[string]string     `json:"requested_args"`
	Args          map[string]string     `json:"args"`
	SecretArgs    []string              `json:"secret_args"`
	Git           Git                   `json:"git"`
	Actor         Actor                 `json:"actor"`
	PullRequest   Optional[PullRequest] `json:"pull_request"`
	Lineage       Lineage               `json:"lineage"`
	EnvNames      []string              `json:"env_names"`
	Reproducer    []string              `json:"reproducer"`
}

// safety: DisplayRequest redacts all requested values until trusted describe supplies classification.
// A malformed classification fails closed. This copy never changes inputs retained for execution.
func DisplayRequest(SealedRequest) Display { panic("unimplemented: c1-data") }

// safety: DisplaySpec applies resolved secret classification to both requested and resolved args.
// Every dashboard, event, public API and CLI renderer uses this projection, never raw Spec JSON.
func DisplaySpec(Spec) Display { panic("unimplemented: c1-data") }
