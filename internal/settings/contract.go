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

// Values combines native run preferences with parent-only startup inputs.
type Values struct {
	runspec.Options
	ConfigPath        *string
	Home              *string
	Args              map[string]string
	Guards            *projectconfig.Guards
	Admission         *userconfig.AdmissionConfig
	Toolchain         *string
	BoxID             *string
	Cache             *userconfig.CacheConfig
	SecretsKey        *KeyInput
	CatchUp           *time.Duration
	RunnerPreferences *userconfig.RunnerConfig
	Retention         *RetentionValues
	IdlePoll          *time.Duration
	Connection        *userconfig.Connection
	TokenTTL          *time.Duration
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
	ceilings    *runspec.Ceilings
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
