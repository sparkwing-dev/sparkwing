package client

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// SetPriority re-ranks a queued run the daemon is arbitrating. mode is empty
// for an absolute priority, or "front"/"back" to resolve the rank against the
// waiters that are not part of the run.
func (cl *Client) SetPriority(ctx context.Context, runID string, priority int, mode string) (wingwire.SetPriorityAck, error) {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	retry := newRetry("set priority", readOnlyRetryLimit)
	for {
		ack, terminal, transient := cl.readSetPriority(runID, priority, mode)
		if transient == nil {
			return ack, terminal
		}
		if err := retry.wait(ctx, transient); err != nil {
			return wingwire.SetPriorityAck{}, err
		}
		if rerr := cl.recoverConn(ctx); rerr != nil {
			return wingwire.SetPriorityAck{}, rerr
		}
	}
}

func (cl *Client) readSetPriority(runID string, priority int, mode string) (ack wingwire.SetPriorityAck, terminal, transient error) {
	if err := cl.write(&wingwire.SetPriority{RunID: runID, Priority: priority, Mode: mode}); err != nil {
		return wingwire.SetPriorityAck{}, nil, err
	}
	msg, err := cl.dec.read()
	if err != nil {
		return wingwire.SetPriorityAck{}, nil, err
	}
	if refusal := cl.unsupportedOperation(msg, "re-ranking a queued run"); refusal != nil {
		return wingwire.SetPriorityAck{}, refusal, nil
	}
	got, ok := msg.(*wingwire.SetPriorityAck)
	if !ok {
		return wingwire.SetPriorityAck{}, fmt.Errorf("wingd/client: expected set_priority_ack, got %T", msg), nil
	}
	return *got, nil, nil
}
