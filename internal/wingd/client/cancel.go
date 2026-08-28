package client

import (
	"context"
	"errors"
)

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
