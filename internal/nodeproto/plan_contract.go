package nodeproto

import "encoding/json"

// PlanDoc excludes host-owned state so SDK declarations cannot rewrite sealed authority.
type PlanDoc struct {
	Jobs           []JobSpec         `json:"jobs"`
	Concurrency    []ConcurrencySpec `json:"concurrency,omitempty"`
	Priority       *int              `json:"priority,omitempty"`
	ClaimWaitMS    *int64            `json:"claim_wait_ms,omitempty"`
	AdmissionClass *string           `json:"admission_class,omitempty"`
	Source         *SourceSpec       `json:"source,omitempty"`
}

// JobSpec is flat so every placement validates the same execution envelope.
type JobSpec struct {
	Arguments      []ArgumentDescription `json:"arguments,omitempty"`
	ID             string                `json:"id"`
	Needs          []string              `json:"deps,omitempty"`
	NeedsIfPresent []string              `json:"deps_if_present,omitempty"`
	NeedsGenerated []string              `json:"needs_generated,omitempty"`
	Groups         []string              `json:"groups,omitempty"`
	Env            map[string]EnvValue   `json:"env,omitempty"`
	Artifacts      []string              `json:"artifacts,omitempty"`
	Consumes       []ArtifactInput       `json:"consumes,omitempty"`
	References     []PipelineReference   `json:"pipeline_refs,omitempty"`
	// Children names the pipelines this job may start as child runs; intake refuses any other.
	Children            []ChildPipeline   `json:"children,omitempty"`
	Approval            *ApprovalSpec     `json:"approval,omitempty"`
	OnFailureOf         string            `json:"on_failure_of,omitempty"`
	Retry               *RetrySpec        `json:"retry,omitempty"`
	TimeoutMS           *int64            `json:"timeout_ms,omitempty"`
	NoProgressTimeoutMS *int64            `json:"no_progress_timeout_ms,omitempty"`
	Requires            []string          `json:"requires,omitempty"`
	Prefers             []string          `json:"prefers,omitempty"`
	Inline              bool              `json:"inline,omitempty"`
	Optional            bool              `json:"optional,omitempty"`
	ContinueOnError     bool              `json:"continue_on_error,omitempty"`
	Memo                *MemoSpec         `json:"memo,omitempty"`
	Caches              []CacheSpec       `json:"caches,omitempty"`
	Concurrency         []ConcurrencySpec `json:"concurrency,omitempty"`
	Closures            ClosureSpec       `json:"closures,omitzero"`
	SkipIfBudgetMS      *int64            `json:"skip_if_budget_ms,omitempty"`
	Steps               []StepSpec        `json:"steps,omitempty"`
	ResultStep          string            `json:"result_step,omitempty"`
	FailurePolicy       string            `json:"failure_policy,omitempty"`
}

// ChildPipeline names an allowed child pipeline; an empty repository inherits the parent repository.
type ChildPipeline struct {
	Pipeline   string `json:"pipeline"`
	Repository string `json:"repository,omitempty"`
}

// EnvValue distinguishes interpreter startup from SDK-applied body environment.
type EnvValue struct {
	Value   string `json:"value"`
	Startup bool   `json:"startup,omitempty"`
}

// StepSpec carries author declarations, never a host-computed spec hash or lifecycle state.
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
	Closures          ClosureSpec `json:"closures,omitzero"`
}

// ClosureSpec references executable code; persisted generated specs never serialize closures.
type ClosureSpec struct {
	SkipIf    []string `json:"skip_if,omitempty"`
	BeforeRun []string `json:"before_run,omitempty"`
	Verify    string   `json:"verify,omitempty"`
	AfterRun  []string `json:"after_run,omitempty"`
	MemoKey   string   `json:"memo_key,omitempty"`
	Generate  []string `json:"generate,omitempty"`
}

// ConcurrencySpec declares membership at plan or job scope; the store derives scoped keys.
type ConcurrencySpec struct {
	Group          string  `json:"group"`
	Scope          *string `json:"scope,omitempty"`
	Capacity       *int    `json:"capacity,omitempty"`
	Cost           *int    `json:"cost,omitempty"`
	OnLimit        *string `json:"on_limit,omitempty"`
	QueueTimeoutMS *int64  `json:"queue_timeout_ms,omitempty"`
}

// SourceSpec expresses checkout policy without granting repository authority.
type SourceSpec struct {
	FullHistory *bool `json:"full_history,omitempty"`
	Depth       *int  `json:"depth,omitempty"`
	Tags        *bool `json:"tags,omitempty"`
	Submodules  *bool `json:"submodules,omitempty"`
	LFS         *bool `json:"lfs,omitempty"`
}

// RetrySpec counts application retries only; disruptions do not consume this budget.
type RetrySpec struct {
	Attempts  int    `json:"attempts"`
	BackoffMS *int64 `json:"backoff_ms,omitempty"`
}

// MemoSpec enables result reuse independently of dependency-directory caches.
type MemoSpec struct {
	TTLMS *int64     `json:"ttl_ms,omitempty"`
	Input *MemoInput `json:"input,omitempty"`
}

// CacheSpec is wire-only so SDK binaries need not link engine storage implementations.
type CacheSpec struct {
	Name     string   `json:"name"`
	Path     string   `json:"path,omitempty"`
	Resolver string   `json:"resolver,omitempty"`
	KeyScope string   `json:"key_scope,omitempty"`
	KeyFiles []string `json:"key_files"`
}

// ApprovalSpec keeps zero timeout indefinite; the owner's deadline remains separate.
type ApprovalSpec struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS *int64 `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

// ArtifactInput declares the dependency edge used to authorize artifact reads.
type ArtifactInput struct {
	Producer string `json:"producer"`
	Into     string `json:"into,omitempty"`
}

// PipelineReference declares a cross-run read without granting undeclared blob access.
type PipelineReference struct {
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
}

// SecretMatcher avoids copying secret values into validation errors or persisted plans.
type SecretMatcher interface{ ContainsSecret(identifier string) bool }

// ValidationContext supplies host facts without admitting those fields in SDK JSON.
type ValidationContext struct {
	Placement           Placement
	Secrets             SecretMatcher
	Expansion           bool
	ExistingJobs        []JobSpec
	ExistingConcurrency []ConcurrencySpec
	SourceJob           string
}

// ValidatePlan strictly decodes plans and expansions, rejecting invalid graphs and secret-bearing identifiers.
func ValidatePlan(raw json.RawMessage, context ValidationContext) (PlanDoc, error) {
	plan, err := validatePlan(raw, context)
	return plan, sanitizePlanError(err, context.Secrets)
}

// MemoInput declares inputs evaluated by the executing host against its checkout and job environment.
type MemoInput struct {
	Kind     string      `json:"kind"`
	Value    string      `json:"value,omitempty"`
	Patterns []string    `json:"patterns,omitempty"`
	Root     string      `json:"root,omitempty"`
	Names    []string    `json:"names,omitempty"`
	Parts    []any       `json:"parts,omitempty"`
	Inputs   []MemoInput `json:"inputs,omitempty"`
}
