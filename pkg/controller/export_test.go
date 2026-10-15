package controller

import (
	"context"
	"testing"

	"golang.org/x/crypto/ssh"
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
