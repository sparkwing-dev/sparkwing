package launcher

import (
	"context"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// CloudUnitKind selects host work without weakening the common Cloud isolation contract.
type CloudUnitKind string

const (
	CloudPlan CloudUnitKind = "plan"
	CloudEval CloudUnitKind = "eval"
	CloudWork CloudUnitKind = "work"
)

// CloudJobConfig accepts operator-owned images and ceilings, never pipeline-supplied pod fragments.
type CloudJobConfig struct {
	Labels                                             []string
	Namespace, RunnerImage, DockerImage, ControllerURL string
	CPUCeiling                                         float64
	MemoryCeiling                                      int64
}

// CloudJobBinding is derived from an awarded row, not the launcher's requested team or fence.
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

// LeaseRef returns the awarded unit fence, including the eval lease generation.
func (binding CloudJobBinding) LeaseRef() capability.LeaseRef {
	lease := capability.LeaseRef{ID: binding.UnitID, Holder: binding.Holder, Generation: binding.Generation}
	switch binding.Kind {
	case CloudPlan:
		lease.Kind = capability.UnitPlan
	case CloudEval:
		lease.Kind = capability.UnitEval
	case CloudWork:
		lease.Kind = capability.UnitNode
	}
	return lease
}

// CloudJobAward carries only a single-use bootstrap, never the launcher's root credential.
type CloudJobAward struct {
	LeaseExpiresAt time.Time        `json:"lease_expires_at"`
	Engine         *engine.Identity `json:"engine,omitempty"`
	Source         source.Spec      `json:"source"`
	Binding        CloudJobBinding  `json:"binding"`
	Bootstrap      []byte           `json:"-"`
	CPUCores       float64          `json:"cpu_cores"`
	Memory         int64            `json:"memory_bytes"`
}

// CloudNodeReservation is an exclusive placement fact held until the Job and DinD have stopped.
type CloudNodeReservation struct {
	NodeName, NodeUID, Team, JobName, ReservationID string
	Award                                           capability.LeaseRef
}

// CloudNetworkEvidence comes from enforcement probes, not the presence of NetworkPolicy YAML.
type CloudNetworkEvidence struct {
	Namespace, NodeUID, PolicyRevision string
	DefaultDenyEgressEnforced          bool
	RequiredDestinationsAllowed        bool
	DaemonSetPortsInventoried          bool
	EscapeProbePassed                  bool
}

// CloudBoundary reserves one Job per team-dedicated node and verifies enforced network isolation.
type CloudBoundary interface {
	Reserve(context.Context, CloudJobBinding, string) (CloudNodeReservation, CloudNetworkEvidence, error)
	Release(context.Context, CloudNodeReservation) error
	Retire(context.Context, CloudNodeReservation) error
}

// CloudJobResources couples Secret cleanup and node ownership with the Job it authorizes.
type CloudJobResources struct {
	Job             *batchv1.Job
	BootstrapSecret *corev1.Secret
	Reservation     CloudNodeReservation
	Network         CloudNetworkEvidence
	JobDeadline     time.Time
}
