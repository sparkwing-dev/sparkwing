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
	sourceRun       Source = "run"
	projectPipeline Source = "project.pipeline"
	projectDefaults Source = "project.defaults"
	code            Source = "code"
	machine         Source = "machine"
	Default         Source = "default"
	teamDB          Source = "operator.team"
	GlobalDB        Source = "operator.global"
	limitsProfile   Source = "operator.profile"
)

// precedence gives closer explicit sources priority; ceilings remain independent.
func precedence() []Source {
	return []Source{Flag, Env, sourceRun, projectPipeline, projectDefaults, code, machine, Default}
}

// operatorPrecedence keeps a named flag authoritative, including explicit zero.
func operatorPrecedence() []Source {
	return []Source{Flag, teamDB, GlobalDB, limitsProfile, Default}
}

// Origin keeps source, file, key and line when a value moves into the sealed run spec.
type Origin struct {
	Source Source `json:"source"`
	File   string `json:"file,omitempty"`
	Key    string `json:"key"`
	Line   int    `json:"line,omitempty"`
}

// phase prevents project and code settings from being frozen before planning.
type phase string

// Resolution phases name when a preference is complete, not when storage seals it.
const (
	startup    phase = "startup"
	intake     phase = "intake"
	acceptPlan phase = "accept_plan"
)

// rule is the precedence-document and conformance-test source for one setting.
type rule struct {
	Key     string
	Sources []Source
	Phase   phase
}

// Values supplies the settings resolved by this package.
type Values struct {
	runspec.Options
	Args              map[string]string
	Guards            *projectconfig.Guards
	RunnerPreferences *userconfig.RunnerConfig
	Origins           map[string]Origin
}

// OperatorValues uses pointers because zero disables a documented guard; it never
// means missing.
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
	NumCPU              int
	Terminal            bool
	ParentCheckout      string
	RegisteredCheckouts []string
	FallbackPaths       []string
	ScheduleArgs        map[string]string
	ScheduleHostArgs    map[string]string
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

// originLayers is the complete C4-to-C1 mapping; operator sources remain distinguishable.
var originLayers = map[Source]runspec.Layer{
	Flag: runspec.LayerFlag, Env: runspec.LayerEnv, sourceRun: runspec.LayerSealed,
	projectPipeline: runspec.LayerProjectPipeline, projectDefaults: runspec.LayerProjectDefaults,
	code: runspec.LayerCode, machine: runspec.LayerMachine, Default: runspec.LayerDefault,
	teamDB: runspec.LayerOperatorTeam, GlobalDB: runspec.LayerOperatorGlobal,
	limitsProfile: runspec.LayerOperatorProfile,
}
