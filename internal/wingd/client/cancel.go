package client

import (
	"context"
	"errors"
)

// Cancel asks the local admission daemon to cancel a run by id without
// spawning one. It returns (true, nil) when the daemon knew the run and
// signalled it to wind down, (false, nil) when no daemon is running or the
// daemon does not know the run (the caller should fall back to the
// controller), and an error only on a transport failure.
func Cancel(ctx context.Context, opts Options, runID string) (bool, error) {
	noSpawn := opts
	noSpawn.Spawn = func(string, string) error { return ErrNoDaemon }
	cl, err := EnsureDaemon(ctx, noSpawn)
	if err != nil {
		if errors.Is(err, ErrNoDaemon) {
			return false, nil
		}
		return false, err
	}
	defer cl.Close()
	return cl.CancelLease(ctx, runID)
}
