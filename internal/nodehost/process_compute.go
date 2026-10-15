package nodehost

import (
	"context"
	"sync"
)

type processCompute struct {
	mu   sync.Mutex
	runs map[string]int
}

func (c *processCompute) active(runID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[runID] > 0
}

func (c *processCompute) start(runID string, start func() (ExecutorProcess, error)) (ExecutorProcess, error) {
	c.mu.Lock()
	if c.runs == nil {
		c.runs = make(map[string]int)
	}
	c.runs[runID]++
	c.mu.Unlock()
	stop := sync.OnceFunc(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.runs[runID]--
		if c.runs[runID] == 0 {
			delete(c.runs, runID)
		}
	})
	process, err := start()
	if process == nil {
		stop()
		return nil, err
	}
	return &computingProcess{ExecutorProcess: process, stop: stop}, err
}

type computingProcess struct {
	ExecutorProcess
	stop func()
}

func (p *computingProcess) Cleanup(ctx context.Context) error {
	defer p.stop()
	return p.ExecutorProcess.Cleanup(ctx)
}
