package client

import (
	"context"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

var _ controllerwire.QueueManager = (*engineClient)(nil)

// NewQueueManager reads queue projections and changes group priority using
// the authenticated controller transport, including api.sock transports.
func NewQueueManager(options EngineOptions) (controllerwire.QueueManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) ReadQueue(ctx context.Context) (wingwire.QueueState, error) {
	var result wingwire.QueueState
	err := c.request(ctx, "GET", "/api/v1/queue", "/api/v1/queue", "", nil, &result)
	return result, err
}

func (c *engineClient) ReadQueueGroup(ctx context.Context, group string) (controllerwire.QueueGroup, error) {
	var result controllerwire.QueueGroup
	if err := controllerwire.ValidateQueueGroupName(group); err != nil {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	err := c.request(ctx, "GET", "/api/v1/queue/groups/{group}", "/api/v1/queue/groups/"+url.PathEscape(group), "", nil, &result)
	if err == nil && !validQueueGroup(result, group) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an inconsistent queue group"}
	}
	return result, err
}

func (c *engineClient) SetGroupPriority(ctx context.Context, group string, input controllerwire.QueuePriorityWrite) (controllerwire.QueuePriorityResult, error) {
	var result controllerwire.QueuePriorityResult
	if err := controllerwire.ValidateQueueGroupName(group); err != nil {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	if err := input.Validate(); err != nil {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	if input.IdempotencyKey == "" || strings.ContainsAny(input.IdempotencyKey, "\r\n\x00") {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "group priority requires an idempotency key"}
	}
	err := c.request(ctx, "PUT", "/api/v1/queue/groups/{group}/priority", "/api/v1/queue/groups/"+url.PathEscape(group)+"/priority", input.IdempotencyKey, input, &result)
	if err == nil && (result.Group != group || result.RunID == "" || result.Position < 0 || result.PreviousPosition < 0 || result.Participants < 0 || result.Found && input.Priority != nil && result.Priority != *input.Priority) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an inconsistent priority result"}
	}
	return result, err
}

func validQueueGroup(result controllerwire.QueueGroup, group string) bool {
	if result.Group != group || result.Capacity < 0 || result.EffectiveCapacity < 0 || result.UsedCost < 0 || result.Holders == nil || result.Waiters == nil {
		return false
	}
	holders := make(map[string]bool, len(result.Holders))
	for _, h := range result.Holders {
		if h.HolderID == "" || h.RunID == "" || h.Cost < 0 || h.ClaimedAt.IsZero() || h.LeaseExpiresAt.IsZero() || !h.LeaseExpiresAt.After(h.ClaimedAt) || holders[h.HolderID] {
			return false
		}
		holders[h.HolderID] = true
	}
	for i, w := range result.Waiters {
		if w.RunID == "" || w.ArrivedAt.IsZero() || w.Position != i+1 || w.Cost < 0 || w.CancelTimeout < 0 || w.Policy == "" {
			return false
		}
	}
	return true
}
