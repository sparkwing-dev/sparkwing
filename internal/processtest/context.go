// Package processtest orders real subprocess events for ownership regressions.
package processtest

import (
	"context"
	"sync"
)

type beforeDone struct {
	context.Context
	once   sync.Once
	before func()
}

// BeforeDone runs before once when the owner first asks for its cancellation channel.
func BeforeDone(ctx context.Context, before func()) context.Context {
	return &beforeDone{Context: ctx, before: before}
}

func (c *beforeDone) Done() <-chan struct{} {
	c.once.Do(c.before)
	return c.Context.Done()
}
