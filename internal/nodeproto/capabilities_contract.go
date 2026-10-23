package nodeproto

// Placement identifies execution topology without overloading runner labels or trust pools.
type Placement string

// PlacementLocal through PlacementGitHubActions name the supported execution topologies.
const (
	PlacementLocal          Placement = "local"
	PlacementLocalTriggered Placement = "local_triggered"
	PlacementRunnerHosted   Placement = "runner_hosted"
	PlacementLauncher       Placement = "launcher"
	PlacementHere           Placement = "here"
	PlacementGitHubActions  Placement = "github_actions"
)

// planCapability names each declarative feature checked by the shared validator.
type planCapability string

// capabilityJobs through capabilitySource name the declarative features checked during validation.
const (
	capabilityJobs                  planCapability = "jobs"
	capabilitySteps                 planCapability = "steps"
	capabilityDependencies          planCapability = "dependencies"
	capabilityOptionalDependencies  planCapability = "optional_dependencies"
	capabilityGroups                planCapability = "groups"
	capabilityGenerators            planCapability = "generators"
	capabilityGeneratedDependencies planCapability = "generated_dependencies"
	capabilityRecovery              planCapability = "recovery"
	capabilityRetry                 planCapability = "retry"
	capabilityTimeout               planCapability = "timeout"
	capabilityNoProgressTimeout     planCapability = "no_progress_timeout"
	capabilityMemo                  planCapability = "memo"
	capabilityCaches                planCapability = "caches"
	capabilityArtifacts             planCapability = "artifacts"
	capabilityConsumes              planCapability = "consumes"
	capabilityReferences            planCapability = "references"
	capabilityRequires              planCapability = "requires"
	capabilityPrefers               planCapability = "prefers"
	capabilityInline                planCapability = "inline"
	capabilityJobConcurrency        planCapability = "job_concurrency"
	capabilityPlanConcurrency       planCapability = "plan_concurrency"
	capabilityBoxConcurrency        planCapability = "box_concurrency"
	capabilityApproval              planCapability = "approval"
	capabilityApprovalCondition     planCapability = "approval_condition"
	capabilityClosures              planCapability = "closures"
	capabilitySkipBudget            planCapability = "skip_budget"
	capabilityFailurePolicy         planCapability = "failure_policy"
	capabilityRisk                  planCapability = "risk"
	capabilityDryRun                planCapability = "dry_run"
	capabilityStartupEnv            planCapability = "startup_env"
	capabilityBodyEnv               planCapability = "body_env"
	capabilityPriority              planCapability = "priority"
	capabilityClaimWait             planCapability = "claim_wait"
	capabilityAdmissionClass        planCapability = "admission_class"
	capabilitySource                planCapability = "source"
)

// placementCapability records support for one feature across execution topologies.
type placementCapability struct {
	Feature        planCapability
	Local          bool
	LocalTriggered bool
	RunnerHosted   bool
	Launcher       bool
	Here           bool
	GitHubActions  bool
}

// capabilityTable returns the supported features for every execution topology.
func capabilityTable() []placementCapability {
	return []placementCapability{
		{capabilityJobs, true, true, true, true, true, true},
		{capabilitySteps, true, true, true, true, true, true},
		{capabilityDependencies, true, true, true, true, true, true},
		{capabilityOptionalDependencies, true, true, true, true, true, true},
		{capabilityGroups, true, true, true, true, true, true},
		{capabilityGenerators, true, true, true, true, true, true},
		{capabilityGeneratedDependencies, true, true, true, true, true, true},
		{capabilityRecovery, true, true, true, true, true, true},
		{capabilityRetry, true, true, true, true, true, true},
		{capabilityTimeout, true, true, true, true, true, true},
		{capabilityNoProgressTimeout, true, true, true, true, true, true},
		{capabilityMemo, true, true, true, true, true, true},
		{capabilityCaches, true, true, true, true, true, true},
		{capabilityArtifacts, true, true, true, true, true, true},
		{capabilityConsumes, true, true, true, true, true, true},
		{capabilityReferences, true, true, true, true, true, true},
		{capabilityRequires, true, true, true, true, true, true},
		{capabilityPrefers, true, true, true, true, true, true},
		{capabilityInline, true, true, true, true, true, true},
		{capabilityJobConcurrency, true, true, true, true, true, true},
		{capabilityPlanConcurrency, true, true, true, true, true, true},
		{capabilityBoxConcurrency, true, true, true, true, true, true},
		{capabilityApproval, true, true, true, true, true, true},
		{capabilityApprovalCondition, true, true, true, true, true, true},
		{capabilityClosures, true, true, true, true, true, true},
		{capabilitySkipBudget, true, true, true, true, true, true},
		{capabilityFailurePolicy, true, true, true, true, true, true},
		{capabilityRisk, true, true, true, true, true, true},
		{capabilityDryRun, true, true, true, true, true, true},
		{capabilityStartupEnv, true, true, true, true, true, true},
		{capabilityBodyEnv, true, true, true, true, true, true},
		{capabilityPriority, true, true, true, true, true, true},
		{capabilityClaimWait, true, true, true, true, true, true},
		{capabilityAdmissionClass, true, true, true, true, true, true},
		{capabilitySource, true, true, true, true, true, true},
	}
}
