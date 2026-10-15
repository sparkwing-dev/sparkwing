package controllerwire

import "context"

// RunDeletion preserves the controller's deletion result, including a no-op
// replay. Retention pins and financial records remain controller/store policy.
type RunDeletion struct {
	RunID   string `json:"run_id"`
	Deleted bool   `json:"deleted"`
}

// RunPruner uses public observations to select runs and the authorized DELETE
// route to remove them. A local client never opens the controller's database.
type RunPruner interface {
	RunReader
	DeleteRun(context.Context, string) (RunDeletion, error)
}
