package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func (cl *Client) ResetStats(ctx context.Context) error {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	retry := newRetry("stats reset", readOnlyRetryLimit)
	for {
		terminal, transient := cl.readResetStats()
		if transient == nil {
			return terminal
		}
		if err := retry.wait(ctx, transient); err != nil {
			return err
		}
		if rerr := cl.recoverConn(ctx); rerr != nil {
			return rerr
		}
	}
}

func (cl *Client) readResetStats() (terminal, transient error) {
	if err := cl.write(&wingwire.StatsReset{}); err != nil {
		return nil, err
	}
	msg, err := cl.dec.read()
	if err != nil {
		return nil, err
	}
	if _, ok := msg.(*wingwire.StatsResetAck); !ok {
		return fmt.Errorf("wingd/client: expected stats_reset_ack, got %T", msg), nil
	}
	return nil, nil
}

func ResetStats(ctx context.Context, opts Options) error {
	noSpawn := opts
	noSpawn.Spawn = func(string, string) error { return ErrNoDaemon }
	cl, err := EnsureDaemon(ctx, noSpawn)
	if err != nil {
		if errors.Is(err, ErrNoDaemon) {
			return ErrNoDaemon
		}
		return err
	}
	defer cl.Close()
	return cl.ResetStats(ctx)
}
