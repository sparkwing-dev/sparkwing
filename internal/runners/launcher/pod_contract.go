package launcher

import (
	"context"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// safety: CloudUnitKind selects host work without weakening the common Cloud isolation contract.
type CloudUnitKind string

const (
	CloudPlan CloudUnitKind = "plan"
	CloudEval CloudUnitKind = "eval"
	CloudWork CloudUnitKind = "work"
)

// safety: CloudUnitPolicy makes Docker and bootstrap delivery mandatory even for planning and evaluation.
type CloudUnitPolicy struct {
	Kind                     CloudUnitKind
	RunAsUser                int64
	PrivilegedDockerSidecar  bool
	BootstrapSecretFile      bool
	SourceInitContainer      bool
	WorkCapabilityMemoryOnly bool
	ScratchBytes             int64
}

// safety: CloudUnitPolicies covers every Cloud Job; callers cannot opt out of root or DinD.
func CloudUnitPolicies() []CloudUnitPolicy {
	return []CloudUnitPolicy{
		{CloudPlan, 0, true, true, true, true, limits.JobScratchBytes},
		{CloudEval, 0, true, true, true, true, limits.JobScratchBytes},
		{CloudWork, 0, true, true, true, true, limits.JobScratchBytes},
	}
}

// safety: CloudJobConfig accepts operator-owned images and ceilings, never pipeline-supplied pod fragments.
// Both images must be digest-pinned. Scratch is fixed by limits.JobScratchBytes, without a setting.
type CloudJobConfig struct {
	Namespace, RunnerImage, DockerImage, ControllerURL string
	CPUCeiling                                         float64
	MemoryCeiling                                      int64
}

// safety: CloudJobBinding is derived from an awarded row, not the launcher's requested team or fence.
// Eval SourceNodeID and SourceGeneration bind expansion to the immutable terminal source generation.
// UnitID is persisted with the Job for controller reconciliation after launcher restart.
type CloudJobBinding struct {
	Kind                           CloudUnitKind
	UnitID                         string
	Team, RunID, NodeID, Holder    string
	Generation, Attempt            int64
	SourceNodeID                   string
	SourceGeneration               int64
	OwnerDeadline, AttemptDeadline time.Time
	ParentCapabilityExpiry         time.Time
}

// safety: CloudJobAward carries only a single-use bootstrap, never the launcher's root credential.
// Credentials are excluded from JSON and must not appear in diagnostics, argv or environment.
// Engine is required for eval/work; a first plan resolves its pin after source acquisition.
type CloudJobAward struct {
	Engine    *engine.Identity `json:"engine,omitempty"`
	Source    source.Spec      `json:"source"`
	Binding   CloudJobBinding  `json:"binding"`
	Bootstrap []byte           `json:"-"`
	CPUCores  float64          `json:"cpu_cores"`
	Memory    int64            `json:"memory_bytes"`
}

// safety: CloudNodeReservation is an exclusive placement fact held until the Job and DinD have stopped.
// Team dedication and exclusivity are mandatory constraints, never scheduler preferences.
type CloudNodeReservation struct {
	NodeName, NodeUID, Team, JobName, ReservationID string
}

// safety: CloudNetworkEvidence comes from enforcement probes, not the presence of NetworkPolicy YAML.
// Evidence must cover the target namespace/node and current policy revision before execution starts.
type CloudNetworkEvidence struct {
	Namespace, NodeUID, PolicyRevision string
	DefaultDenyEgressEnforced          bool
	RequiredDestinationsAllowed        bool
	DaemonSetPortsInventoried          bool
	EscapeProbePassed                  bool
}

// safety: CloudBoundary reserves one Job per team-dedicated node and verifies enforced network isolation.
// Reserve refuses shared tenancy or missing enforcement evidence. Release waits for pod teardown.
// Node reuse must clear prior Job state before another reservation; privileged Docker is not isolation.
type CloudBoundary interface {
	Reserve(context.Context, CloudJobBinding, string) (CloudNodeReservation, CloudNetworkEvidence, error)
	Release(context.Context, CloudNodeReservation) error
}

// safety: CloudJobResources couples Secret cleanup and node ownership with the Job it authorizes.
// The Secret is mounted only into fetch-source and run-node, never the Docker sidecar.
type CloudJobResources struct {
	Job             *batchv1.Job
	BootstrapSecret *corev1.Secret
	Reservation     CloudNodeReservation
	Network         CloudNetworkEvidence
	JobDeadline     time.Time
	ClaimDeadline   time.Time
}

// safety: CloudPodInvariant names an acceptance obligation for every builder output.
type CloudPodInvariant struct {
	Name, Requirement string
}

// safety: CloudPodInvariants is the complete common pod contract, including external execution gates.
func CloudPodInvariants() []CloudPodInvariant {
	return []CloudPodInvariant{
		{"root", "plan, eval and work processes run as uid 0"},
		{"docker", "each Job has a privileged DinD sidecar and Docker is ready before host execution"},
		{"dedicated_node", "hard-bind to the verified reservation: one Job per team-dedicated node"},
		{"network", "default-deny egress is enforced with explicit required destinations; validate enforcement, DaemonSet ports and escape canary"},
		{"scratch", "all Job scratch, including Docker storage, shares the fixed 20 GiB aggregate budget"},
		{"bootstrap", "per-Job Secret file supplies source.read and claim.exchange only; launcher RBAC permits create/delete, never get"},
		{"source", "fetch-source init reads the bootstrap file and source.Acquire checks out the sealed SHA exactly"},
		{"engine", "plan resolves the exact Go/TypeScript SDK pin after checkout; eval/work use the accepted engine identity; GitHub release verification and machine caching precede execution; runner image version never substitutes for the pin"},
		{"exchange", "run-node exchanges once before SDK execution; revoke bootstrap atomically and keep the resulting unit capability in memory"},
		{"ancestry", "exchange retains the claimant-root parent, server-derived team/binding and fixed grants for the unit kind"},
		{"argv", "--controller, --run, --job, --generation, --source-dir, --module-cache and --bootstrap-file replace plumbing env"},
		{"credentials", "no bearer in Job env or argv; bootstrap file is owner-only and is not mounted in DinD"},
		{"service_account", "use the customer Job service account without API-token automount or cloud-role annotation"},
		{"host_access", "no host network, host PID/IPC namespace or hostPath mounts; Docker uses Job-local storage"},
		{"deadlines", "Job and launch claim derive from attempt plus limits.JobSlack, capped by owner deadline and parent capability expiry"},
		{"retry", "application retries retain the same Job and claim; OOMKilled is ordinary job failure, never a disruption"},
		{"logs", "plan and eval output uses the run log sink, as work output does"},
		{"teardown", "delete the bootstrap Secret on all exit paths and release the node only after all Job containers stop"},
	}
}

// safety: BuildJob is the sole builder for plan, eval and work, including run-host-dispatched work.
// It validates the award and ceilings, calls limits.JobDeadline, then reserves the boundary before
// returning any runnable object. Failure releases acquired reservations and leaves no reusable Secret.
// Claim expiry must cover the Job and never exceed its parent; expired inputs refuse launch.
func BuildJob(ctx context.Context, now time.Time, cfg CloudJobConfig, award CloudJobAward, boundary CloudBoundary) (CloudJobResources, error) {
	return buildJob(ctx, now, cfg, award, boundary)
}
