package controller

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

// TeamBoundaryExempt exposes the routes the team boundary leaves to their own
// gate, so the external boundary test can hold every other run route to it.
var TeamBoundaryExempt = teamBoundaryExempt

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

// StubTrustedFetch replaces the controller's source and module fetches for
// the rest of t, since a test has no GitHub to fetch from.
func StubTrustedFetch(t *testing.T,
	checkout func(ctx context.Context, repoURL, sha, branch, dest string, cred bincache.DirectCredential, o bincache.SourceOptions) error,
	fetch func(ctx context.Context, repoURL, rev, dir string, cred bincache.DirectCredential) (bincache.ModuleCommit, error),
	tags func(ctx context.Context, repoURL, prefix, dir string, cred bincache.DirectCredential) ([]string, error),
) {
	c, f, g := checkoutSource, fetchModuleCommit, moduleTags
	checkoutSource, fetchModuleCommit, moduleTags = checkout, fetch, tags
	t.Cleanup(func() { checkoutSource, fetchModuleCommit, moduleTags = c, f, g })
}

// VerifyLiveDataGrant exposes the check every controller use of a cache
// grant makes, reporting whether the grant is limited to binary uploads.
func VerifyLiveDataGrant(ctx context.Context, s *Server, raw string) (bool, error) {
	grant, err := authwire.VerifyCacheGrant(os.Getenv(authwire.CacheGrantKeyEnv), raw, time.Now())
	if err != nil {
		return false, err
	}
	return s.verifyLiveDataGrant(ctx, grant, false)
}
