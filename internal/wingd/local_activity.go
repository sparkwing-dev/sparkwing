package wingd

import (
	"errors"
	"fmt"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func (d *Daemon) dashboardConnection(delta int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dashboardConns += delta
	d.idleSince = time.Now()
}

func (d *Daemon) setLocalActivity(activity LocalActivity) error {
	if activity.Runs < 0 || activity.QueuedTriggers < 0 || activity.HeldLeases < 0 || activity.DashboardConnections < 0 {
		return fmt.Errorf("wingd: local activity counts must be nonnegative")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if activity != (LocalActivity{}) || d.localActivity != activity {
		d.idleSince = time.Now()
	}
	d.localActivity = activity
	return nil
}

func (d *Daemon) reportLocalRunner(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// safety: a stopping controller refuses runner starts on purpose; that refusal says nothing about the host.
	if d.draining || d.shuttingDown {
		return
	}
	if err == nil {
		d.runnerState, d.runnerErr = LocalRunnerRunning, nil
		return
	}
	d.runnerState, d.runnerErr = LocalRunnerUnavailable, localRunnerFailure(err, d.cfg.Home)
}

func localRunnerFailure(err error, home string) *apierr.Error {
	var failure *apierr.Error
	if errors.As(err, &failure) {
		return failure
	}
	hint := "The controller retries with backoff of up to a minute"
	if log, logErr := LogPath(home); logErr == nil {
		hint += " and logs each attempt in " + log
	}
	return &apierr.Error{Code: hosterr.Unavailable, Message: "the local runner cannot start: " + err.Error(), Hint: hint}
}
