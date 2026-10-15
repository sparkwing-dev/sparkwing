// Package settings owns precedence so hosts consume sealed settings instead of
// re-reading their own environment or machine files after a run is admitted.
package settings

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// Source preserves the origin needed to explain a refusal without leaking values.
type Source string

// Sources are ordered by Precedence; operator sources use OperatorPrecedence.
const (
	Flag            Source = "flag"
	Env             Source = "env"
	Run             Source = "run"
	ProjectPipeline Source = "project.pipeline"
	ProjectDefaults Source = "project.defaults"
	Code            Source = "code"
	Machine         Source = "machine"
	Default         Source = "default"
	TeamDB          Source = "operator.team"
	GlobalDB        Source = "operator.global"
	LimitsProfile   Source = "operator.profile"
)

// Precedence gives closer explicit sources priority; ceilings remain independent.
func Precedence() []Source {
	return []Source{Flag, Env, Run, ProjectPipeline, ProjectDefaults, Code, Machine, Default}
}

// OperatorPrecedence keeps a named flag authoritative, including explicit zero.
func OperatorPrecedence() []Source {
	return []Source{Flag, TeamDB, GlobalDB, LimitsProfile, Default}
}

// Origin keeps source, file, key and line when a value moves into the sealed run spec.
type Origin struct {
	Source Source `json:"source"`
	File   string `json:"file,omitempty"`
	Key    string `json:"key"`
	Line   int    `json:"line,omitempty"`
}

// Phase prevents project and code settings from being frozen before planning.
type Phase string

// Resolution phases name when a preference is complete, not when storage seals it.
const (
	Startup    Phase = "startup"
	Intake     Phase = "intake"
	AcceptPlan Phase = "accept_plan"
)

// Rule is the precedence-document and conformance-test source for one setting.
type Rule struct {
	Key        string
	Resolver   string
	Sources    []Source
	Phase      Phase
	Merge      string
	Zero       string
	Validation string
}

// Resources uses native units so hosts cannot disagree through unit conversion.
type Resources struct {
	Cores       *float64 `json:"cores,omitempty"`
	MemoryBytes *int64   `json:"memory_bytes,omitempty"`
}

// PauseSelection is sealed request data instead of inherited debug env.
type PauseSelection struct {
	Before    []string `json:"before,omitempty"`
	After     []string `json:"after,omitempty"`
	OnFailure *bool    `json:"on_failure,omitempty"`
}

// Values retains presence until acceptance. Nil is unset; explicit zero and
// empty collections remain values. Origins is keyed by the keys in Table.
// This is a settings projection, not an independently sealed run specification.
type Values struct {
	ConfigPath        *string
	Home              *string
	Profile           *string
	Args              map[string]string
	Guards            *projectconfig.Guards
	Requires          []string
	Prefers           []string
	Workers           *int
	Output            *string
	AdmissionClass    *string
	Priority          *int
	Admission         *userconfig.AdmissionConfig
	Toolchain         *string
	PauseTimeout      *time.Duration
	BoxID             *string
	Cache             *userconfig.CacheConfig
	SecretsKey        *KeyInput
	CatchUp           *time.Duration
	RunnerPreferences *userconfig.RunnerConfig
	RunnerSelection   *runspec.RunnerSelection
	StartAt           *string
	StopAt            *string
	Only              *string
	DryRun            *bool
	NoMemo            *bool
	AllowRisk         []string
	OwnerExecution    *time.Duration
	JobTimeout        *time.Duration
	NoProgressTimeout *time.Duration
	RetryBackoff      *time.Duration
	ApprovalWait      *time.Duration
	Retry             *int
	MemoTTL           *time.Duration
	ClaimWait         *time.Duration
	Retention         *RetentionValues
	IdlePoll          *time.Duration
	Connection        *userconfig.Connection
	RepoLocation      *string
	Pause             *PauseSelection
	Debug             *bool
	Publish           *string
	CacheFrom         *string
	Resources         *Resources
	Checkout          *nodeproto.SourceSpec
	TokenTTL          *time.Duration
	SubmitEnvAllow    []string
	Origins           map[string]Origin
}

// KeyInput is consumed in the parent process; diagnostics may show only presence.
// The previous key remains independent so rotation can still decrypt old records.
type KeyInput struct {
	Current  *string
	File     *string
	Previous *string
}

// RetentionValues keeps metric and run retention separate from fixed blob/log rules.
// Zero run or metric days disables that sweep; financial records never cascade.
type RetentionValues struct {
	RunDays    *int
	MetricDays *int
}

// OperatorValues uses pointers because zero disables a documented guard; it never
// means missing. MaxRunsPerPrincipalHour is the sole per-principal run-rate guard.
type OperatorValues struct {
	RunRetentionDays          *int
	MetricRetentionDays       *int
	OwnerExecution            *time.Duration
	MaxCores                  *float64
	MaxMemoryBytes            *int64
	MaxPriority               *int
	AllowedAdmissionClasses   []string
	AllowedSources            []nodeproto.SourceSpec
	MaxClaimWait              *time.Duration
	MaxConcurrentRunners      *int
	MaxGlobalRunners          *int
	RunnerAlarm               *int
	MaxJobsPerRun             *int
	MaxGlobalJobsPerRun       *int
	MaxRunsPerPrincipalHour   *int
	MaxGlobalRunsPerHour      *int
	MinScheduleInterval       *time.Duration
	ClaimsPerRunnerMinute     *int
	HeartbeatsPerRunnerMinute *int
	RequestsPerTokenMinute    *int
	RequestsPerMinuteAlarm    *int
	MaxLogStreamsPerPrincipal *int
	MaxDownloadsPerPrincipal  *int
	ShedQueueDepth            *int
	EgressDailyAlarmBytes     *int64
	EnforceIdleClaimPoll      *bool
	Origins                   map[string]Origin
}

// OperatorInputs keeps deployment authority out of author preferences.
type OperatorInputs struct {
	Flags   OperatorValues
	Team    OperatorValues
	Global  OperatorValues
	Profile OperatorValues
}

// Context carries facts used for defaults without reading process-global state.
// Schedule and host argument overrides become request args before intake seals.
type Context struct {
	Origin              runspec.Origin
	Submitter           bool
	Local               bool
	ControllerOwned     bool
	Detached            bool
	Cloud               bool
	NumCPU              int
	Hostname            string
	UserHome            string
	XDGConfigHome       string
	Terminal            bool
	ParentCheckout      string
	RegisteredCheckouts []string
	FallbackPaths       []string
	ScheduleArgs        map[string]string
	ScheduleHostArgs    map[string]string
	OwnerDeadline       time.Time
}

// Inputs has a fixed field for each source, rather than caller-ordered layers.
// Non-submitters require the sealed projection and never consult local preferences
// for accepted settings. Machine and env values are captured at intake once.
type Inputs struct {
	Flags       Values
	Environment Values
	Sealed      Values
	Pipeline    Values
	Defaults    Values
	Code        Values
	Machine     Values
	Operator    OperatorInputs
	Context     Context
}

// Error names invalid or conflicting input without echoing secrets. Above-ceiling
// errors carry both nonsecret values and origins; ambiguous profiles fail closed.
type Error struct {
	Key         string
	From        Origin
	CeilingFrom *Origin
	Value       string
	Ceiling     string
	Reason      string
}

// PlanInputs permits only captured request inputs and checkout-derived declarations.
// Context supplies trusted execution facts, never machine or environment preferences.
// Request and its ceilings are loaded from server-owned intake state; Declarations is the
// submitted restricted checkout projection, never unrestricted Values or premerged winners.
type PlanInputs struct {
	Request      runspec.SealedRequest
	Declarations runspec.CheckoutDeclarations
	Plan         nodeproto.PlanDoc
	Context      Context
}

// ExpansionInputs prevents generated jobs from consulting new host preferences.
type ExpansionInputs struct {
	Spec    runspec.Spec
	Plan    nodeproto.PlanDoc
	Context Context
}

// OriginLayers is the complete C4-to-C1 mapping; operator sources remain distinguishable.
var OriginLayers = map[Source]runspec.Layer{
	Flag: runspec.LayerFlag, Env: runspec.LayerEnv, Run: runspec.LayerSealed,
	ProjectPipeline: runspec.LayerProjectPipeline, ProjectDefaults: runspec.LayerProjectDefaults,
	Code: runspec.LayerCode, Machine: runspec.LayerMachine, Default: runspec.LayerDefault,
	TeamDB: runspec.LayerOperatorTeam, GlobalDB: runspec.LayerOperatorGlobal,
	LimitsProfile: runspec.LayerOperatorProfile,
}

// ProjectionField names a complete request-field mapping without reimplementing precedence.
type ProjectionField struct {
	ValueField  string
	OptionField string
	OriginKey   string
}

// RequestProjection excludes startup inputs and separately resolved args/guards/schedules.
// Nested resource, pause and checkout origins use the key plus their field name.
var RequestProjection = []ProjectionField{
	{"RunnerSelection", "Runner", "runner"},
	{"Resources", "Resources", "resources"},
	{"Priority", "Priority", "priority"},
	{"AdmissionClass", "AdmissionClass", "admission_class"},
	{"ClaimWait", "ClaimWait", "claim_wait"},
	{"OwnerExecution", "OwnerExecutionTimeout", "owner_execution"},
	{"Checkout", "Checkout", "source"},
	{"Requires", "Requires", "requires"},
	{"Prefers", "Prefers", "prefers"},
	{"StartAt", "StartAt", "start_at"},
	{"StopAt", "StopAt", "stop_at"},
	{"Only", "Only", "only"},
	{"DryRun", "DryRun", "dry_run"},
	{"NoMemo", "NoMemo", "no_memo"},
	{"AllowRisk", "AllowRisk", "allow_risk"},
	{"Workers", "Workers", "workers"},
	{"Output", "Output", "output"},
	{"Pause", "Pause", "debug.pause"},
	{"PauseTimeout", "PauseTimeout", "debug.pause_timeout"},
	{"Debug", "Debug", "debug"},
	{"Publish", "Publish", "publish"},
	{"CacheFrom", "CacheFrom", "cache_from"},
	{"SubmitEnvAllow", "SubmitEnvAllow", "run.submit_env_allow"},
	{"RepoLocation", "RepoLocation", "repo_location"},
	{"Profile", "Profile", "profile"},
	{"JobTimeout", "JobDefaults.Timeout", "job.timeout"},
	{"NoProgressTimeout", "JobDefaults.NoProgressTimeout", "job.no_progress_timeout"},
	{"ApprovalWait", "JobDefaults.ApprovalWait", "approval.wait"},
	{"Retry", "JobDefaults.Retry", "job.retry"},
	{"RetryBackoff", "JobDefaults.RetryBackoff", "job.retry_backoff"},
	{"MemoTTL", "JobDefaults.MemoTTL", "job.memo_ttl"},
}
