package gotoolchain

import (
	"context"
	"os/exec"
)

// Run owns cmd through cancellation, preserving failures from outside its termination.
func Run(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// safety: one owner must control cancellation; exec.CommandContext must not race its signal attribution.
	cmd.Cancel = nil
	return runToolchain(ctx, cmd)
}
