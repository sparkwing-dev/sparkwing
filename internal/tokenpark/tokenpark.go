// Package tokenpark holds a polling loop the controller refused with a dead
// token, so a revoked credential costs the controller one request an hour
// rather than one per poll, and re-enrolling takes effect without a restart.
package tokenpark

import (
	"context"
	"os"
	"time"
)

// WatchInterval is how often a parked loop looks for a rewritten
// configuration. A stat is local, so the cost is the machine's, not the
// controller's.
const WatchInterval = 5 * time.Second

// Wait waits d, until ctx ends, or until changed reports true, and reports
// whether changed did. A nil changed waits d or ctx.
func Wait(ctx context.Context, d time.Duration, changed func() bool) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	var tick <-chan time.Time
	if changed != nil {
		t := time.NewTicker(WatchInterval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick:
			if changed() {
				return true
			}
		}
	}
}

// FileChanged returns a func reporting whether path's modification time or
// size differs from when FileChanged was called, or whether the file appeared
// or vanished since.
func FileChanged(path string) func() bool {
	before, beforeErr := os.Stat(path)
	return func() bool {
		now, err := os.Stat(path)
		if err != nil || beforeErr != nil {
			return (err == nil) != (beforeErr == nil)
		}
		return !now.ModTime().Equal(before.ModTime()) || now.Size() != before.Size()
	}
}
