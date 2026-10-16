package localhostwire

import (
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// LocalDoctor carries cleanup findings even when another cleanup fails.
type LocalDoctor struct {
	Findings []wingd.LocalDoctorFinding `json:"findings"`
	Failure  *apierr.Error              `json:"failure,omitempty"`
}
