package capability

import (
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Validate checks the award fence without granting authority to its reported state.
func (o LauncherObservation) Validate() error {
	if o.Award.ID == "" || o.Award.Holder == "" || o.Award.Generation <= 0 {
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher observation requires a complete award fence"}
	}
	switch o.Award.Kind {
	case UnitPlan, UnitEval, UnitNode:
		if o.CapacityUnconfigured && (!o.Reservation || o.Rejected) {
			return &apierr.Error{Code: apierr.BadRequest, Message: "unconfigured capacity requires a demand observation"}
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
