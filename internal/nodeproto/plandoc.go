// Package nodeproto holds the wire types the engine and an SDK process
// exchange under the node protocol (docs/node-protocol.md). It imports no
// SDK package, so the Go SDK and the engine can both depend on it.
package nodeproto

import (
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// PlanDoc is the plan document: one run's nodes, their edges and each node's
// execution envelope, as $defs/plan in docs/schemas/describe.schema.json
// declares it.
type PlanDoc struct {
	// Protocol is the node protocol version the document was written for, the
	// value the Sparkwing-Node-Protocol header carries.
	Protocol       string     `json:"protocol,omitempty"`
	Pipeline       string     `json:"pipeline"`
	RunID          string     `json:"run_id"`
	Priority       int        `json:"priority,omitempty"`
	ClaimWaitMS    int64      `json:"claim_wait_ms,omitempty"`
	AdmissionClass string     `json:"admission_class,omitempty"`
	Requires       []string   `json:"requires,omitempty"`
	Nodes          []PlanNode `json:"nodes"`
	PlanConc       *PlanConc  `json:"plan_concurrency,omitempty"`
	PlanConcs      []PlanConc `json:"plan_concurrency_groups,omitempty"`

	Resources *PlanResources `json:"plan_resources,omitempty"`

	Secrets pipelines.SecretsField `json:"secrets,omitempty"`

	Source *PlanSource `json:"source,omitempty"`
}

type PlanSource struct {
	Depth      int  `json:"depth"`
	Tags       bool `json:"tags,omitempty"`
	Submodules bool `json:"submodules,omitempty"`
	LFS        bool `json:"lfs,omitempty"`
}

type PlanNode struct {
	ID           string   `json:"id"`
	Deps         []string `json:"deps"`
	OptionalDeps []string `json:"optional_deps,omitempty"`
	// NeedsGenerated lists generator closure ids; the node waits for every
	// node each of those generators adds.
	NeedsGenerated []string          `json:"needs_generated,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Outputs        []string          `json:"outputs,omitempty"`
	Consumes       []PlanConsume     `json:"consumes,omitempty"`

	Groups   []string      `json:"groups,omitempty"`
	Dynamic  bool          `json:"dynamic,omitempty"`
	Approval *PlanApproval `json:"approval,omitempty"`

	OnFailureOf string `json:"on_failure_of,omitempty"`

	Modifiers *PlanModifiers `json:"modifiers,omitempty"`

	// safety: beside modifiers, not in them, because the controller decodes
	// modifiers strictly and an older one would refuse the whole plan.
	Caches []depcache.Spec `json:"caches,omitempty"`

	MemoKey  *PlanMemoKey  `json:"memo_key,omitempty"`
	Closures *PlanClosures `json:"closures,omitempty"`

	Work *PlanWork `json:"work,omitempty"`

	SpecHash string `json:"spec_hash,omitempty"`

	PipelineRefs []PlanPipelineRef `json:"pipeline_refs,omitempty"`
}

type PlanPipelineRef struct {
	Pipeline string `json:"pipeline"`
	Node     string `json:"node"`
}

type PlanConsume struct {
	Producer string `json:"producer"`
	Into     string `json:"into,omitempty"`
}

// PlanConc is one plan-level concurrency group the whole run holds. Key is
// already scoped, so its prefix says whether the group is box- or run-scoped.
type PlanConc struct {
	Key             string `json:"key,omitempty"`
	Capacity        int    `json:"capacity,omitempty"`
	Cost            int    `json:"cost,omitempty"`
	OnLimit         string `json:"on_limit,omitempty"`
	QueueTimeoutMS  int64  `json:"queue_timeout_ms,omitempty"`
	CancelTimeoutMS int64  `json:"cancel_timeout_ms,omitempty"`
}

type PlanResources struct {
	Cores       float64 `json:"cores,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
}

type PlanApproval struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

type PlanModifiers struct {
	Retry               int      `json:"retry,omitempty"`
	RetryBackoffMS      int64    `json:"retry_backoff_ms,omitempty"`
	RetryAuto           bool     `json:"retry_auto,omitempty"`
	TimeoutMS           int64    `json:"timeout_ms,omitempty"`
	NoProgressTimeoutMS int64    `json:"no_progress_timeout_ms,omitempty"`
	RunsOn              []string `json:"runs_on,omitempty"`
	Prefers             []string `json:"prefers,omitempty"`
	WhenRunner          []string `json:"when_runner,omitempty"`

	Cache      bool  `json:"cache,omitempty"`
	CacheTTLMS int64 `json:"cache_ttl_ms,omitempty"`

	ConcGroup           string `json:"conc_group,omitempty"`
	ConcCapacity        int    `json:"conc_capacity,omitempty"`
	ConcCost            int    `json:"conc_cost,omitempty"`
	ConcScope           string `json:"conc_scope,omitempty"`
	ConcOnLimit         string `json:"conc_on_limit,omitempty"`
	ConcQueueTimeoutMS  int64  `json:"conc_queue_timeout_ms,omitempty"`
	ConcCancelTimeoutMS int64  `json:"conc_cancel_timeout_ms,omitempty"`

	ResCores        float64 `json:"res_cores,omitempty"`
	ResMemoryBytes  int64   `json:"res_memory_bytes,omitempty"`
	Inline          bool    `json:"inline,omitempty"`
	Optional        bool    `json:"optional,omitempty"`
	ContinueOnError bool    `json:"continue_on_error,omitempty"`
	OnFailure       string  `json:"on_failure,omitempty"`
	HasBeforeRun    bool    `json:"has_before_run,omitempty"`
	HasAfterRun     bool    `json:"has_after_run,omitempty"`
	HasSkipIf       bool    `json:"has_skip_if,omitempty"`
	HasVerify       bool    `json:"has_verify,omitempty"`
	SkipIfBudgetMS  int64   `json:"skip_if_budget_ms,omitempty"`
}

type PlanWork struct {
	Steps         []PlanStep      `json:"steps,omitempty"`
	StepGroups    []PlanStepGroup `json:"step_groups,omitempty"`
	ResultStep    string          `json:"result_step,omitempty"`
	FailurePolicy string          `json:"failure_policy,omitempty"`
}

type PlanStepGroup struct {
	Name    string   `json:"name,omitempty"`
	Members []string `json:"members"`
}

type PlanStep struct {
	ID        string   `json:"id"`
	Needs     []string `json:"needs,omitempty"`
	IsResult  bool     `json:"is_result,omitempty"`
	HasSkipIf bool     `json:"has_skip_if,omitempty"`
	Finally   bool     `json:"finally,omitempty"`

	Risks []string `json:"risks,omitempty"`

	ContinueOnError   bool          `json:"continue_on_error,omitempty"`
	Optional          bool          `json:"optional,omitempty"`
	HasDryRun         bool          `json:"has_dry_run,omitempty"`
	SafeWithoutDryRun bool          `json:"safe_without_dry_run,omitempty"`
	Closures          *PlanClosures `json:"closures,omitempty"`
}

// PlanMemoKey names the inputs the host hashes into a node's memo key.
type PlanMemoKey struct {
	// Closure is the id of the node's cache_key closure.
	Closure string `json:"closure,omitempty"`
}

// PlanClosures lists a node's or a step's closure ids by kind. The host
// treats each id as opaque and passes it back in an eval request.
type PlanClosures struct {
	SkipIf    []string `json:"skip_if,omitempty"`
	BeforeRun []string `json:"before_run,omitempty"`
	AfterRun  []string `json:"after_run,omitempty"`
	Verify    string   `json:"verify,omitempty"`
	CacheKey  string   `json:"cache_key,omitempty"`
	Generator []string `json:"generator,omitempty"`
}
