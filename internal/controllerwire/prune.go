package controllerwire

import "context"

// RunPruner selects runs through public observations and acknowledges authorized deletion.
type RunPruner interface {
	RunReader
	DeleteRun(context.Context, string) error
}
