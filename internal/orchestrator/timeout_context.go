package orchestrator

import (
	"context"
	"sync"
	"time"
)

type nodeTimeoutDurationKey struct{}
type nodeParentContextKey struct{}
type nodeTimeoutControllerKey struct{}

type nodeTimeoutController struct {
	parent context.Context

	mu        sync.Mutex
	done      chan struct{}
	err       error
	timer     *time.Timer
	deadline  time.Time
	remaining time.Duration
	paused    bool
	timerGen  uint64

	deadlineInspector func() bool
	inspectorGen      uint64
}

func newNodeTimeoutContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx := &nodeTimeoutController{
		parent:   parent,
		done:     make(chan struct{}),
		deadline: time.Now().Add(timeout),
	}
	ctx.armTimer(timeout)
	go func() {
		select {
		case <-parent.Done():
			ctx.finish(parent.Err())
		case <-ctx.done:
		}
	}()
	return context.WithValue(ctx, nodeTimeoutControllerKey{}, ctx), func() {
		ctx.finish(context.Canceled)
	}
}

func nodeTimeoutControllerFromContext(ctx context.Context) *nodeTimeoutController {
	controller, _ := ctx.Value(nodeTimeoutControllerKey{}).(*nodeTimeoutController)
	return controller
}

func withNodeTimeoutDuration(ctx context.Context, timeout time.Duration) context.Context {
	if timeout <= 0 {
		return ctx
	}
	return context.WithValue(ctx, nodeTimeoutDurationKey{}, timeout)
}

func nodeTimeoutDurationFromContext(ctx context.Context) time.Duration {
	timeout, _ := ctx.Value(nodeTimeoutDurationKey{}).(time.Duration)
	return timeout
}

func withNodeParentContext(ctx context.Context, parent context.Context) context.Context {
	if parent == nil {
		return ctx
	}
	return context.WithValue(ctx, nodeParentContextKey{}, parent)
}

func nodeParentContextFromContext(ctx context.Context) context.Context {
	parent, _ := ctx.Value(nodeParentContextKey{}).(context.Context)
	if parent == nil {
		return ctx
	}
	return parent
}

func (c *nodeTimeoutController) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused || c.deadline.IsZero() {
		return time.Time{}, false
	}
	return c.deadline, true
}

func (c *nodeTimeoutController) Done() <-chan struct{} {
	return c.done
}

func (c *nodeTimeoutController) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *nodeTimeoutController) Value(key any) any {
	if key == (nodeTimeoutControllerKey{}) {
		return c
	}
	return c.parent.Value(key)
}

func (c *nodeTimeoutController) pauseAt(queuedAt time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.paused {
		return false
	}
	if queuedAt.IsZero() {
		queuedAt = time.Now()
	}
	remaining := c.deadline.Sub(queuedAt)
	if remaining <= 0 {
		return false
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timerGen++
	c.remaining = remaining
	c.paused = true
	c.deadline = time.Time{}
	return true
}

func (c *nodeTimeoutController) isPaused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

func (c *nodeTimeoutController) setDeadlineInspector(inspector func() bool) func() {
	c.mu.Lock()
	c.inspectorGen++
	generation := c.inspectorGen
	c.deadlineInspector = inspector
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.inspectorGen == generation {
			c.deadlineInspector = nil
		}
		c.mu.Unlock()
	}
}

func (c *nodeTimeoutController) resumeAt(admittedAt time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || !c.paused {
		return false
	}
	remaining := c.remaining
	if !admittedAt.IsZero() {
		spentAfterAdmission := time.Since(admittedAt)
		if spentAfterAdmission > 0 {
			remaining -= spentAfterAdmission
		}
	}
	if remaining <= 0 {
		c.finishLocked(context.DeadlineExceeded)
		return false
	}
	c.remaining = 0
	c.paused = false
	c.deadline = time.Now().Add(remaining)
	c.armTimerLocked(remaining)
	return true
}

func (c *nodeTimeoutController) accountCompletedAdmission(queuedAt, admittedAt time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.paused || queuedAt.IsZero() || admittedAt.IsZero() {
		return false
	}
	remaining := c.deadline.Sub(queuedAt)
	spentAfterAdmission := time.Since(admittedAt)
	if spentAfterAdmission > 0 {
		remaining -= spentAfterAdmission
	}
	if remaining <= 0 {
		c.finishLocked(context.DeadlineExceeded)
		return false
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	c.deadline = time.Now().Add(remaining)
	c.armTimerLocked(remaining)
	return true
}

func (c *nodeTimeoutController) armTimer(timeout time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armTimerLocked(timeout)
}

func (c *nodeTimeoutController) armTimerLocked(timeout time.Duration) {
	c.timerGen++
	generation := c.timerGen
	c.timer = time.AfterFunc(timeout, func() {
		c.finishDeadline(generation)
	})
}

func (c *nodeTimeoutController) finishDeadline(generation uint64) {
	c.mu.Lock()
	if c.err != nil || c.paused || c.timerGen != generation {
		c.mu.Unlock()
		return
	}
	inspector := c.deadlineInspector
	c.mu.Unlock()
	if inspector != nil && inspector() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.paused || c.timerGen != generation {
		return
	}
	c.finishLocked(context.DeadlineExceeded)
}

func (c *nodeTimeoutController) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finishLocked(err)
}

func (c *nodeTimeoutController) finishLocked(err error) {
	if c.err != nil {
		return
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timerGen++
	c.err = err
	close(c.done)
}
