package nodeproto

import "encoding/json"

// safety: PlanDoc excludes host-owned state so SDK declarations cannot rewrite sealed authority.
type PlanDoc struct {
	Jobs           []JobSpec         `json:"jobs"`
	Concurrency    []ConcurrencySpec `json:"concurrency,omitempty"`
	Resources      *ResourceSpec     `json:"resources,omitempty"`
	Priority       *int              `json:"priority,omitempty"`
	ClaimWaitMS    *int64            `json:"claim_wait_ms,omitempty"`
	AdmissionClass *string           `json:"admission_class,omitempty"`
	Source         *SourceSpec       `json:"source,omitempty"`
}

// safety: JobSpec is flat so every placement validates the same execution envelope.
type JobSpec struct {
	ID                  string              `json:"id"`
	Needs               []string            `json:"deps,omitempty"`
	NeedsIfPresent      []string            `json:"deps_if_present,omitempty"`
	NeedsGenerated      []string            `json:"needs_generated,omitempty"`
	Groups              []string            `json:"groups,omitempty"`
	Env                 map[string]EnvValue `json:"env,omitempty"`
	Artifacts           []string            `json:"artifacts,omitempty"`
	Consumes            []ArtifactInput     `json:"consumes,omitempty"`
	References          []PipelineReference `json:"pipeline_refs,omitempty"`
	Approval            *ApprovalSpec       `json:"approval,omitempty"`
	OnFailureOf         string              `json:"on_failure_of,omitempty"`
	Retry               *RetrySpec          `json:"retry,omitempty"`
	TimeoutMS           *int64              `json:"timeout_ms,omitempty"`
	NoProgressTimeoutMS *int64              `json:"no_progress_timeout_ms,omitempty"`
	Requires            []string            `json:"requires,omitempty"`
	Prefers             []string            `json:"prefers,omitempty"`
	Inline              bool                `json:"inline,omitempty"`
	Optional            bool                `json:"optional,omitempty"`
	ContinueOnError     bool                `json:"continue_on_error,omitempty"`
	Memo                *MemoSpec           `json:"memo,omitempty"`
	Caches              []CacheSpec         `json:"caches,omitempty"`
	Concurrency         []ConcurrencySpec   `json:"concurrency,omitempty"`
	Resources           *ResourceSpec       `json:"resources,omitempty"`
	Closures            ClosureSpec         `json:"closures,omitempty"`
	SkipIfBudgetMS      *int64              `json:"skip_if_budget_ms,omitempty"`
	Steps               []StepSpec          `json:"steps,omitempty"`
	ResultStep          string              `json:"result_step,omitempty"`
	FailurePolicy       string              `json:"failure_policy,omitempty"`
}

// safety: EnvValue distinguishes interpreter startup from SDK-applied body environment.
type EnvValue struct {
	Value   string `json:"value"`
	Startup bool   `json:"startup,omitempty"`
}

// safety: StepSpec carries author declarations, never a host-computed spec hash or lifecycle state.
type StepSpec struct {
	ID                string      `json:"id"`
	Needs             []string    `json:"needs,omitempty"`
	Groups            []string    `json:"groups,omitempty"`
	Finally           bool        `json:"finally,omitempty"`
	Risks             []string    `json:"risks,omitempty"`
	Optional          bool        `json:"optional,omitempty"`
	ContinueOnError   bool        `json:"continue_on_error,omitempty"`
	HasDryRun         bool        `json:"has_dry_run,omitempty"`
	SafeWithoutDryRun bool        `json:"safe_without_dry_run,omitempty"`
	Closures          ClosureSpec `json:"closures,omitempty"`
}

// safety: ClosureSpec references executable code; persisted generated specs never serialize closures.
type ClosureSpec struct {
	SkipIf    []string `json:"skip_if,omitempty"`
	BeforeRun []string `json:"before_run,omitempty"`
	Verify    string   `json:"verify,omitempty"`
	AfterRun  []string `json:"after_run,omitempty"`
	MemoKey   string   `json:"memo_key,omitempty"`
	Generate  []string `json:"generate,omitempty"`
}

// safety: ConcurrencySpec is shared at plan and job scope; only the host computes scoped keys.
type ConcurrencySpec struct {
	Group           string `json:"group"`
	Scope           string `json:"scope"`
	Capacity        int    `json:"capacity"`
	Cost            int    `json:"cost"`
	OnLimit         string `json:"on_limit"`
	QueueTimeoutMS  *int64 `json:"queue_timeout_ms,omitempty"`
	CancelTimeoutMS *int64 `json:"cancel_timeout_ms,omitempty"`
}

// safety: ResourceSpec preserves unset values until the setting's seal point.
type ResourceSpec struct {
	Cores       *float64 `json:"cores,omitempty"`
	MemoryBytes *int64   `json:"memory_bytes,omitempty"`
}

// safety: SourceSpec expresses checkout policy without granting repository authority.
type SourceSpec struct {
	FullHistory *bool `json:"full_history,omitempty"`
	Depth       *int  `json:"depth,omitempty"`
	Tags        *bool `json:"tags,omitempty"`
	Submodules  *bool `json:"submodules,omitempty"`
	LFS         *bool `json:"lfs,omitempty"`
}

// safety: RetrySpec counts application retries only; disruptions do not consume this budget.
type RetrySpec struct {
	Attempts  int    `json:"attempts"`
	BackoffMS *int64 `json:"backoff_ms,omitempty"`
}

// safety: MemoSpec enables result reuse independently of dependency-directory caches.
type MemoSpec struct {
	TTLMS *int64 `json:"ttl_ms,omitempty"`
}

// safety: CacheSpec is wire-only so SDK binaries need not link engine storage implementations.
type CacheSpec struct {
	Name     string   `json:"name"`
	Path     string   `json:"path,omitempty"`
	Resolver string   `json:"resolver,omitempty"`
	KeyScope string   `json:"key_scope,omitempty"`
	KeyFiles []string `json:"key_files"`
}

// safety: ApprovalSpec keeps zero timeout indefinite; the owner's deadline remains separate.
type ApprovalSpec struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS *int64 `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

// safety: ArtifactInput declares the dependency edge used to authorize artifact reads.
type ArtifactInput struct {
	Producer string `json:"producer"`
	Into     string `json:"into,omitempty"`
}

// safety: PipelineReference declares a cross-run read without granting undeclared blob access.
type PipelineReference struct {
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
}

// safety: SecretMatcher avoids copying secret values into validation errors or persisted plans.
type SecretMatcher interface{ ContainsSecret(identifier string) bool }

// safety: PlanPolicy lets sealed-spec and ceiling owners validate without importing the store into the wire package.
type PlanPolicy interface {
	ValidateResolvedPlan(plan PlanDoc) error
}

// safety: ValidationContext supplies host facts without admitting those fields in SDK JSON.
type ValidationContext struct {
	Policy       PlanPolicy
	Placement    Placement
	Secrets      SecretMatcher
	Expansion    bool
	ExistingJobs []JobSpec
	SourceJob    string
}

// safety: ValidatePlan is the only strict decoder for initial plans and generated expansions.
// It rejects unknown/duplicate keys, invalid graphs and secret-bearing IDs before persistence.
// Errors implement C2 apierr's error contract; the full invariant list is in c3-protocol-sdk.md.
func ValidatePlan(raw json.RawMessage, context ValidationContext) (PlanDoc, error) {
	panic("unimplemented: c3-protocol-sdk")
}
