package controllerwire

import (
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Validate checks the award fence without granting authority to its reported state.
func (o LauncherObservation) Validate() error {
	if o.Award.ID == "" || o.Award.Holder == "" || o.Award.Generation <= 0 {
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher observation requires a complete award fence"}
	}
	switch o.Award.Kind {
	case capability.UnitPlan, capability.UnitEval, capability.UnitNode:
		if o.Reservation && o.Rejected {
			return &apierr.Error{Code: apierr.BadRequest, Message: "launcher reservation cannot report a rejected Job"}
		}
		return nil
	default:
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher observation has an invalid unit kind"}
	}
}
