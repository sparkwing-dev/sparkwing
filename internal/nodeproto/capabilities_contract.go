package nodeproto

// safety: Placement identifies execution topology without overloading runner labels or trust pools.
type Placement string

// safety: Every launch topology participates in the same validation matrix.
const (
	PlacementLocal          Placement = "local"
	PlacementLocalTriggered Placement = "local_triggered"
	PlacementRunnerHosted   Placement = "runner_hosted"
	PlacementLauncher       Placement = "launcher"
	PlacementHere           Placement = "here"
	PlacementGitHubActions  Placement = "github_actions"
)

// safety: PlanCapability names each declarative feature checked by the shared validator.
type PlanCapability string

// safety: Capability names are wire-independent; a host must not silently ignore unsupported features.
const (
	CapabilityJobs                  PlanCapability = "jobs"
	CapabilitySteps                 PlanCapability = "steps"
	CapabilityDependencies          PlanCapability = "dependencies"
	CapabilityOptionalDependencies  PlanCapability = "optional_dependencies"
	CapabilityGroups                PlanCapability = "groups"
	CapabilityGenerators            PlanCapability = "generators"
	CapabilityGeneratedDependencies PlanCapability = "generated_dependencies"
	CapabilityRecovery              PlanCapability = "recovery"
	CapabilityRetry                 PlanCapability = "retry"
	CapabilityTimeout               PlanCapability = "timeout"
	CapabilityNoProgressTimeout     PlanCapability = "no_progress_timeout"
	CapabilityMemo                  PlanCapability = "memo"
	CapabilityCaches                PlanCapability = "caches"
	CapabilityArtifacts             PlanCapability = "artifacts"
	CapabilityConsumes              PlanCapability = "consumes"
	CapabilityReferences            PlanCapability = "references"
	CapabilityRequires              PlanCapability = "requires"
	CapabilityPrefers               PlanCapability = "prefers"
	CapabilityInline                PlanCapability = "inline"
	CapabilityJobConcurrency        PlanCapability = "job_concurrency"
	CapabilityPlanConcurrency       PlanCapability = "plan_concurrency"
	CapabilityBoxConcurrency        PlanCapability = "box_concurrency"
	CapabilityResources             PlanCapability = "resources"
	CapabilityApproval              PlanCapability = "approval"
	CapabilityApprovalCondition     PlanCapability = "approval_condition"
	CapabilityClosures              PlanCapability = "closures"
	CapabilitySkipBudget            PlanCapability = "skip_budget"
	CapabilityFailurePolicy         PlanCapability = "failure_policy"
	CapabilityRisk                  PlanCapability = "risk"
	CapabilityDryRun                PlanCapability = "dry_run"
	CapabilityStartupEnv            PlanCapability = "startup_env"
	CapabilityBodyEnv               PlanCapability = "body_env"
	CapabilityPriority              PlanCapability = "priority"
	CapabilityClaimWait             PlanCapability = "claim_wait"
	CapabilityAdmissionClass        PlanCapability = "admission_class"
	CapabilitySource                PlanCapability = "source"
)

// safety: PlacementCapability makes all six verdicts reviewable, including former launcher parity gaps.
type PlacementCapability struct {
	Feature        PlanCapability
	Local          bool
	LocalTriggered bool
	RunnerHosted   bool
	Launcher       bool
	Here           bool
	GitHubActions  bool
}

// safety: CapabilityTable is the complete launch matrix, not a snapshot of the old implementation's gaps.
// Decision 99 requires parity; a missing feature must be implemented rather than marked unsupported.
func CapabilityTable() []PlacementCapability {
	return []PlacementCapability{
		{CapabilityJobs, true, true, true, true, true, true},
		{CapabilitySteps, true, true, true, true, true, true},
		{CapabilityDependencies, true, true, true, true, true, true},
		{CapabilityOptionalDependencies, true, true, true, true, true, true},
		{CapabilityGroups, true, true, true, true, true, true},
		{CapabilityGenerators, true, true, true, true, true, true},
		{CapabilityGeneratedDependencies, true, true, true, true, true, true},
		{CapabilityRecovery, true, true, true, true, true, true},
		{CapabilityRetry, true, true, true, true, true, true},
		{CapabilityTimeout, true, true, true, true, true, true},
		{CapabilityNoProgressTimeout, true, true, true, true, true, true},
		{CapabilityMemo, true, true, true, true, true, true},
		{CapabilityCaches, true, true, true, true, true, true},
		{CapabilityArtifacts, true, true, true, true, true, true},
		{CapabilityConsumes, true, true, true, true, true, true},
		{CapabilityReferences, true, true, true, true, true, true},
		{CapabilityRequires, true, true, true, true, true, true},
		{CapabilityPrefers, true, true, true, true, true, true},
		{CapabilityInline, true, true, true, true, true, true},
		{CapabilityJobConcurrency, true, true, true, true, true, true},
		{CapabilityPlanConcurrency, true, true, true, true, true, true},
		{CapabilityBoxConcurrency, true, true, true, true, true, true},
		{CapabilityResources, true, true, true, true, true, true},
		{CapabilityApproval, true, true, true, true, true, true},
		{CapabilityApprovalCondition, true, true, true, true, true, true},
		{CapabilityClosures, true, true, true, true, true, true},
		{CapabilitySkipBudget, true, true, true, true, true, true},
		{CapabilityFailurePolicy, true, true, true, true, true, true},
		{CapabilityRisk, true, true, true, true, true, true},
		{CapabilityDryRun, true, true, true, true, true, true},
		{CapabilityStartupEnv, true, true, true, true, true, true},
		{CapabilityBodyEnv, true, true, true, true, true, true},
		{CapabilityPriority, true, true, true, true, true, true},
		{CapabilityClaimWait, true, true, true, true, true, true},
		{CapabilityAdmissionClass, true, true, true, true, true, true},
		{CapabilitySource, true, true, true, true, true, true},
	}
}
