package controllerwire

import (
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"k8s.io/apimachinery/pkg/util/validation"
)

// LauncherJobRef requires a UID precondition when deleting the named Kubernetes Job.
type LauncherJobRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
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
		return o.Job.Validate()
	default:
		return &apierr.Error{Code: apierr.BadRequest, Message: "launcher observation has an invalid unit kind"}
	}
}
