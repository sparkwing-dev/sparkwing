package nodehost

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// ProcessCleanups owns one delegated process's registrations until joined cleanup.
type ProcessCleanups struct {
	mu     sync.Mutex
	list   cleanupList
	closed bool
}

func (c *ProcessCleanups) contains(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && slices.ContainsFunc(c.list.items, func(item cleanup) bool { return item.id == id })
}

// Register retains a validated command under the existing cleanup limits.
func (c *ProcessCleanups) Register(argv []string, description string) (string, error) {
	if message := checkCleanup(argv, description); message != "" {
		return "", executorError(apierr.BadRequest, message)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", executorError(apierr.LockHeld, "process cleanup registrations are closed")
	}
	if len(c.list.items) >= maxCleanups {
		return "", executorError(apierr.BadRequest, "process cleanup registrations are full")
	}
	c.list.next++
	id := strconv.Itoa(c.list.next)
	c.list.items = append(c.list.items, cleanup{id: id, argv: slices.Clone(argv), description: description})
	return id, nil
}

// Release removes only this process's outstanding cleanup.
func (c *ProcessCleanups) Release(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return executorError(apierr.LockHeld, "process cleanup registrations are closed")
	}
	index := slices.IndexFunc(c.list.items, func(item cleanup) bool { return item.id == id })
	if index < 0 {
		return executorError(apierr.NotFound, "process has no cleanup with that ID")
	}
	c.list.items = slices.Delete(c.list.items, index, index+1)
	return nil
}

// Run closes registration and runs unreleased commands once, after process revocation and join.
func (c *ProcessCleanups) Run(ctx context.Context, scope CleanupScope) error {
	c.mu.Lock()
	c.closed = true
	items := c.list.items
	c.list.items = nil
	c.mu.Unlock()
	log := scope.Log
	if log == nil {
		log = io.Discard
	}
	var failures []error
	for _, item := range slices.Backward(items) {
		if ctx.Err() != nil {
			break
		}
		if err := runCleanup(ctx, item, scope.Dir, scope.Env, log); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(append(failures, context.Cause(ctx))...)
}
