package controllerwire

import "github.com/sparkwing-dev/sparkwing/internal/runspec"

// NodeRef identifies a run's job within a tenant-scoped request.
type NodeRef struct {
	RunID  runspec.RunID  `json:"run_id"`
	NodeID runspec.NodeID `json:"job_id"`
}
