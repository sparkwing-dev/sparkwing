package capability

import (
	"fmt"
	"math"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// Validate checks the award fence without granting authority to its reported state.
func (o LauncherObservation) Validate() error {
	if o.Award.ID == "" || o.Award.Holder == "" || o.Award.Generation <= 0 {
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher observation requires a complete award fence"}
	}
	switch o.Award.Kind {
	case UnitPlan, UnitEval, UnitNode:
		if (o.CapacityUnconfigured || o.CapacityUnavailable) && (!o.Reservation || o.Rejected) {
			return &apierr.Error{Code: apierr.BadRequest, Message: "a capacity failure requires a demand observation"}
		}
		if o.CapacityUnconfigured && o.CapacityUnavailable {
			return &apierr.Error{Code: apierr.BadRequest, Message: "capacity cannot be both unconfigured and unavailable"}
		}
		if c := o.CapacityCeiling; c != nil && (!o.CapacityUnconfigured || c.Cores <= 0 || c.MemoryBytes <= 0) {
			return &apierr.Error{Code: apierr.BadRequest, Message: "a capacity ceiling requires an unconfigured-capacity failure and positive CPU and memory"}
		}
		if o.Reservation && o.Rejected {
			return &apierr.Error{Code: apierr.BadRequest, Message: "launcher reservation cannot report a rejected Job"}
		}
		return nil
	default:
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher observation has an invalid unit kind"}
	}
}

// Validate refuses activation without a fenced execution reservation.
func (a LauncherActivation) Validate() error {
	if err := (LauncherObservation{Award: a.Award}).Validate(); err != nil {
		return err
	}
	if a.NodeUID == "" || a.ReservationID == "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher activation requires an execution node reservation"}
	}
	if a.Capacity != nil {
		sizes := match.CloudSizes()
		valid := false
		for i, size := range sizes {
			if size.Label == a.Capacity.RequestedClass {
				valid = a.Capacity.GrantedClass == size.Label || i+1 < len(sizes) && a.Capacity.GrantedClass == sizes[i+1].Label
			}
		}
		if !valid {
			return &apierr.Error{Code: apierr.BadRequest, Message: "Cloud capacity grant must match its requested class or the next size"}
		}
	}
	return nil
}

// CloudCapacityGrant reports a launcher's verified hardware allocation. Billing and
// container resource limits continue to use the award's requested class.
type CloudCapacityGrant struct {
	RequestedClass     string `json:"requested_class"`
	GrantedClass       string `json:"granted_class"`
	WaitedForFreshNode bool   `json:"waited_for_fresh_node"`
}

// LauncherObservation reports one award's preparation state without Kubernetes identities.
type LauncherObservation struct {
	Award                LeaseRef `json:"award"`
	Reservation          bool     `json:"reservation,omitempty"`
	Rejected             bool     `json:"rejected,omitempty"`
	WaitedForFreshNode   bool     `json:"waited_for_fresh_node,omitempty"`
	CapacityUnconfigured bool     `json:"capacity_unconfigured,omitempty"`
	CapacityUnavailable  bool     `json:"capacity_unavailable,omitempty"`
	// CapacityCeiling is the launcher's resource ceiling when the request exceeded it.
	CapacityCeiling *match.Resources `json:"capacity_ceiling,omitempty"`
}

// LauncherActivation binds a verified execution reservation before its Job is unsuspended.
type LauncherActivation struct {
	Award         LeaseRef            `json:"award"`
	Capacity      *CloudCapacityGrant `json:"capacity,omitempty"`
	NodeUID       string              `json:"node_uid"`
	ReservationID string              `json:"reservation_id"`
}

// CloudCapacityUnconfigured names a terminal configuration failure before execution.
const CloudCapacityUnconfigured = "cloud_capacity_unconfigured"

// CloudCapacityUnconfiguredMessage gives customers the support path for a missing hardware-class pool.
const CloudCapacityUnconfiguredMessage = "Cloud capacity is not configured for the requested hardware class. Contact support@sparkwing.dev"

// CloudCapacityCeilingMessage names the request and the launcher ceiling it exceeded, with both remedies.
func CloudCapacityCeilingMessage(requested, ceiling match.Resources, runID string) string {
	return fmt.Sprintf("Requested class sparkwing-%dcpu needs %s CPU and %s memory, above this Cloud pool's ceiling of %s CPU and %s memory. Request a smaller hardware class, or contact support@sparkwing.dev to raise the ceiling (Support reference: %s)",
		int64(requested.Cores), strconv.FormatFloat(requested.Cores, 'f', -1, 64), gibibytes(requested.MemoryBytes), strconv.FormatFloat(ceiling.Cores, 'f', -1, 64), gibibytes(ceiling.MemoryBytes), runID)
}

func gibibytes(bytes int64) string {
	return strconv.FormatFloat(math.Round(float64(bytes)/(1<<30)*10)/10, 'f', -1, 64) + " GiB"
}

// CloudCapacityUnavailable names a terminal failure before execution while the
// Cloud budget breaker refuses new job nodes.
const CloudCapacityUnavailable = "cloud_capacity_unavailable"

// CloudCapacityUnavailableMessage gives customers the support path while new Cloud capacity is stopped.
const CloudCapacityUnavailableMessage = "Cloud capacity is temporarily unavailable. Contact support@sparkwing.dev"
