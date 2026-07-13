package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ResetStats asks the daemon to clear its rolling admission-outcome window
// over this client's connection. It is a control operation and creates no
// lease; use it on a dedicated connection, not one holding a lease.
func (cl *Client) ResetStats(ctx context.Context) error {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	for {
		terminal, transient := cl.readResetStats()
		if transient == nil {
			return terminal
		}
		if rerr := cl.recoverConn(ctx); rerr != nil {
			return rerr
		}
	}
}

func (cl *Client) readResetStats() (terminal error, transient error) {
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

// ResetStats connects to a running daemon and clears its admission-outcome
// window without spawning one. When no daemon is running it returns
// [ErrNoDaemon]: there is no window to clear.
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
