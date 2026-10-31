package capability

import (
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
	return nil
}

// LauncherObservation reports one award's preparation state without Kubernetes identities.
type LauncherObservation struct {
	Award                LeaseRef `json:"award"`
	Reservation          bool     `json:"reservation,omitempty"`
	Rejected             bool     `json:"rejected,omitempty"`
	CapacityUnconfigured bool     `json:"capacity_unconfigured,omitempty"`
}

// LauncherActivation binds a verified execution reservation before its Job is unsuspended.
type LauncherActivation struct {
	Award         LeaseRef `json:"award"`
	NodeUID       string   `json:"node_uid"`
	ReservationID string   `json:"reservation_id"`
}

// CloudCapacityUnconfigured names a terminal configuration failure before execution.
const CloudCapacityUnconfigured = "cloud_capacity_unconfigured"

// CloudCapacityUnconfiguredMessage gives customers the support path for missing team pools.
const CloudCapacityUnconfiguredMessage = "Cloud capacity is not configured for this team. Contact support@sparkwing.dev"
