package controller

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CardBillingPass runs one pass of the payment worker, so a test drives its
// schedule instead of waiting on the ticker.
func (s *Server) CardBillingPass(ctx context.Context) { s.cardBillingPass(ctx) }

// MuxRouteScopes returns every route server.go registers on the authenticated
// mux with the scope requireScope gates it on, read from the source so a route
// added later is in the list without anyone adding it.
func MuxRouteScopes(t *testing.T) map[string]string {
	t.Helper()
	return muxRoutes(t, "server.go")
}

// SetHostKeyScan replaces the ssh host key read a new git credential makes,
// so a test serves a key without dialing a host.
func SetHostKeyScan(s *Server, scan func(ctx context.Context, host string, port int) (ssh.PublicKey, error)) {
	s.hostKeyScan = scan
}

// DrainGitHubChecks waits until every check run update s accepted has reached
// GitHub or been given up on.
func DrainGitHubChecks(ctx context.Context, s *Server) error {
	return s.githubApp.checks.idle(ctx)
}

func (r *githubCheckReporter) idle(ctx context.Context) error {
	for {
		r.mu.Lock()
		empty, changed := len(r.jobs) == 0, r.changed
		r.mu.Unlock()
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// ReportGitHubRunState reports runID's state to GitHub as a run finish does.
func ReportGitHubRunState(ctx context.Context, s *Server, runID, status string) {
	s.reportGitHubRunState(ctx, runID, status)
}

// LaunchClaimClock is the time a test claims runID/nodeID at: now, or the
// node's ready_at when that is later.
//
// safety: ready_at is stamped from an earlier wall-clock read, and a host
// clock sync can step the wall clock back past it (WSL2 steps it back about
// 40ms every 30s), so a claim judged at a fresh time.Now() finds no ready
// node and returns nil.
func LaunchClaimClock(t *testing.T, st *store.Store, runID, nodeID string) time.Time {
	t.Helper()
	now := time.Now()
	var readyAt int64
	if err := st.DB().QueryRow(`SELECT COALESCE(ready_at, 0) FROM nodes WHERE run_id = ? AND node_id = ?`,
		runID, nodeID).Scan(&readyAt); err != nil {
		t.Fatalf("ready_at of %s/%s: %v", runID, nodeID, err)
	}
	if ready := time.Unix(0, readyAt); ready.After(now) {
		return ready
	}
	return now
}
