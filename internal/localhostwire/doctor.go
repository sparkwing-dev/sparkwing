package localhostwire

import (
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// LocalDoctor carries cleanup findings even when another cleanup fails.
type LocalDoctor struct {
	Findings []LocalDoctorFinding `json:"findings"`
	Failure  *apierr.Error        `json:"failure,omitempty"`
}
