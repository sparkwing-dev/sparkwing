package client

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// SetPriority changes the durable queue ordering of a run.
func (cl *Client) SetPriority(ctx context.Context, runID string, priority int, mode string) (wingwire.SetPriorityAck, error) {
	var ack wingwire.SetPriorityAck
	err := cl.admissionCall(ctx, http.MethodPost, "/priority", wingwire.SetPriority{RunID: runID, Priority: priority, Mode: mode}, &ack, readOnlyRetryLimit)
	return ack, err
}
