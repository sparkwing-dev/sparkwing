package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/execdiag"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type progressTimeoutControllerKey struct{}

type progressTimeoutController struct {
	context.Context

	mu         sync.Mutex
	done       chan struct{}
	err        error
	timer      *time.Timer
	timerGen   uint64
	timeout    time.Duration
	pauseDepth int
	expired    bool
}

const noProgressDiagnosticEscalationLimit = 5 * time.Second

const noProgressDiagnosticOutputLimit = 16 << 20

func newProgressTimeoutContext(parent context.Context, timeout time.Duration) (context.Context, *progressTimeoutController, context.CancelFunc) {
	controller := &progressTimeoutController{
		Context: parent,
		done:    make(chan struct{}),
		timeout: timeout,
	}
	controller.reset()
	go func() {
		select {
		case <-parent.Done():
			controller.finish(parent.Err())
		case <-controller.done:
		}
	}()
	ctx := context.WithValue(controller, progressTimeoutControllerKey{}, controller)
	ctx = execdiag.WithPolicy(ctx, execdiag.Policy{
		Expired:         controller.timedOut,
		EscalationLimit: noProgressDiagnosticEscalationLimit,
		OutputLimit:     noProgressDiagnosticOutputLimit,
	})
	return ctx, controller, func() { controller.finish(context.Canceled) }
}

func progressTimeoutControllerFromContext(ctx context.Context) *progressTimeoutController {
	controller, _ := ctx.Value(progressTimeoutControllerKey{}).(*progressTimeoutController)
	return controller
}

func pauseProgressTimeout(ctx context.Context) func() {
	controller := progressTimeoutControllerFromContext(ctx)
	if controller == nil {
		return func() {}
	}
	return controller.pause()
}

func (c *progressTimeoutController) Done() <-chan struct{} { return c.done }

func (c *progressTimeoutController) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *progressTimeoutController) Deadline() (time.Time, bool) {
	return c.Context.Deadline()
}

func (c *progressTimeoutController) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.pauseDepth > 0 {
		return
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timerGen++
	generation := c.timerGen
	c.timer = time.AfterFunc(c.timeout, func() { c.finishDeadline(generation) })
}

func (c *progressTimeoutController) pause() func() {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return func() {}
	}
	c.pauseDepth++
	if c.pauseDepth == 1 && c.timer != nil {
		c.timer.Stop()
		c.timerGen++
	}
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.pauseDepth > 0 {
				c.pauseDepth--
			}
			shouldReset := c.pauseDepth == 0 && c.err == nil
			c.mu.Unlock()
			if shouldReset {
				c.reset()
			}
		})
	}
}

func (c *progressTimeoutController) timedOut() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.expired
}

func (c *progressTimeoutController) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

func (c *progressTimeoutController) finishDeadline(generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.pauseDepth > 0 || c.timerGen != generation {
		return
	}
	c.timerGen++
	c.expired = true
	c.err = context.DeadlineExceeded
	close(c.done)
}

type progressLogger struct {
	delegate sparkwing.Logger
	progress *progressTimeoutController
}

func (l progressLogger) Log(level, msg string) {
	l.progress.reset()
	l.delegate.Log(level, msg)
}

func (l progressLogger) Emit(record sparkwing.LogRecord) {
	l.progress.reset()
	l.delegate.Emit(record)
}
