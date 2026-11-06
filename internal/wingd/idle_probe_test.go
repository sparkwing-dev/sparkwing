package wingd_test

import (
	"context"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func probeHome(ctx context.Context, home string) error {
	socket, err := wingd.EnsureSocketPath(home)
	if err != nil {
		return err
	}
	_, err = client.Probe(ctx, socket)
	return err
}

type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
