package controller

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"

	"golang.org/x/crypto/ssh"
)

var (
	EngineAcceptedFixturePlan = func(t *testing.T, st *store.Store, team store.Team, runID string, jobs ...string) {
		engineAcceptedFixturePlan(t, &engineFixture{Store: st}, team, runID, jobs...)
	}
	EngineClaimHTTPFixture = engineClaimHTTPFixture
	EngineClaimHTTP        = engineClaimHTTP
	EngineAdmitBearer      = engineAdmitBearer
	ChildIntakeFixture     = childIntakeFixture
	EngineSQL              = engineSQL
	EngineHTTPRoot         = engineHTTPRoot
	AssertedProvenance     = assertedProvenance
	SealWith               = sealWith
)

// SetHostKeyScan replaces the ssh host key read a new git credential makes,
// so a test serves a key without dialing a host.
func SetHostKeyScan(s *Server, scan func(ctx context.Context, host string, port int) (ssh.PublicKey, error)) {
	s.hostKeyScan = scan
}

// BindTenantPortForTest binds one fake tenant rather than independent route ports.
func BindTenantPortForTest[P any](s *Server, resolve func(context.Context, store.Team) (P, error)) *Server {
	return testOnlyTenantPort(s, resolve)
}

// ProcessGitHubInboxForTest drains due deliveries through the production lease worker.
func ProcessGitHubInboxForTest(s *Server, ctx context.Context, now time.Time) (map[string]struct {
	Status int
	Body   []byte
}, error,
) {
	results := make(map[string]struct {
		Status int
		Body   []byte
	})
	for {
		out, err := s.processNextGitHubDelivery(ctx, now)
		if err != nil || out == nil {
			return results, err
		}
		results[out.digest] = struct {
			Status int
			Body   []byte
		}{out.status, append([]byte(nil), out.body.Bytes()...)}
	}
}

// WithCostRate sets the USD-per-runner-hour rate the receipt builder
// uses to compute compute_cents. source is echoed into the
// receipt's rate_source field for provenance. Zero rate = receipts
// report compute_cents:0, matching unconfigured-profile semantics.
func (s *Server) WithCostRate(rate float64, source string) *Server {
	s.costPerRunnerHour = rate
	s.costRateSource = source
	return s
}

// WithSignUpApprovalNotifier runs fn for each account an operator admits from
// the waitlist, such as to email the person that they are in. A failure is
// logged and does not undo the admission.
func (s *Server) WithSignUpApprovalNotifier(fn func(context.Context, store.Account) error) *Server {
	s.identity.signup.onApproved = fn
	return s
}
