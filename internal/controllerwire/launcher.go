package controllerwire

import (
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// LauncherJobRef requires a UID precondition when deleting the named Kubernetes Job.
type LauncherJobRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// LauncherReservationRef fences release to one award's exact node reservation.
type LauncherReservationRef struct {
	Award         capability.LeaseRef `json:"award"`
	NodeUID       string              `json:"node_uid"`
	ReservationID string              `json:"reservation_id"`
}

// Validate requires a complete reservation fence without a Job reference.
func (r LauncherReservationRef) Validate() error {
	if r.ReservationID == "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher reservation requires its reservation ID"}
	}
	return (LauncherObservation{Award: r.Award, NodeUID: r.NodeUID, ReservationID: r.ReservationID}).Validate()
}

// Validate refuses cleanup identities that could select a replacement Job by name alone.
func (j LauncherJobRef) Validate() error {
	if j.UID == "" || len(validation.IsDNS1123Label(j.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(j.Name)) != 0 {
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher Job requires namespace, name and UID"}
	}
	return nil
}

// Validate checks observation completeness without granting authority to its reported fence.
func (o LauncherObservation) Validate() error {
	if o.Award.ID == "" || o.Award.Holder == "" || o.Award.Generation <= 0 || o.NodeUID == "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher observation requires the complete award and node identity"}
	}
	switch o.Award.Kind {
	case capability.UnitPlan, capability.UnitEval, capability.UnitNode:
		if o.ReservationID != "" {
			if o.Job != (LauncherJobRef{}) || o.Rejected {
				return &apierr.Error{Code: apierr.BadRequest, Message: "launcher reservation observation cannot carry a Job reference"}
			}
			return nil
		}
		return o.Job.Validate()
	default:
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher observation has an invalid unit kind"}
	}
}
