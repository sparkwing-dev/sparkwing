package client

import (
	"context"
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
	if refusal := cl.lacksOperation(msg); refusal != nil {
		return refusal, nil
	}
	if _, ok := msg.(*wingwire.StatsResetAck); !ok {
		return fmt.Errorf("wingd/client: expected stats_reset_ack, got %T", msg), nil
	}
	return nil, nil
}
