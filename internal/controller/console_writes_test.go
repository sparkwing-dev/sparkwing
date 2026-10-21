package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type consoleWritesFake struct {
	calls int
	seen  capability.Authorized
	value any
	err   error
}

type consoleApprovalInput struct {
	IDs    []string
	Oldest int
	Key    string
}

func (f *consoleWritesFake) record(a capability.Authorized, value any) error {
	f.calls++
	f.seen, f.value = a, value
	return f.err
}

func (f *consoleWritesFake) ConsoleGrantCredit(_ context.Context, a capability.Authorized, value store.CreditGrantRequest) (store.CreditGrantResult, error) {
	return store.CreditGrantResult{}, f.record(a, value)
}

func (f *consoleWritesFake) ConsoleSetBillingTrust(_ context.Context, a capability.Authorized, value store.BillingTrustChange) (store.BillingStanding, error) {
	return store.BillingStanding{}, f.record(a, value)
}

func (f *consoleWritesFake) ConsoleDeclineStandingRequest(_ context.Context, a capability.Authorized, id, reason string) (store.StandingRequest, error) {
	return store.StandingRequest{ID: id, Team: store.Team(a.Target.Team), State: store.StandingRequestDeclined}, f.record(a, []string{id, reason})
}

func (f *consoleWritesFake) ConsoleFreeze(_ context.Context, a capability.Authorized, value string) (store.TeamCreditFreeze, error) {
	return store.TeamCreditFreeze{Frozen: true}, f.record(a, value)
}

func (f *consoleWritesFake) ConsoleUnfreeze(_ context.Context, a capability.Authorized, hold, reason string, noFault bool) (store.TeamCreditFreeze, error) {
	return store.TeamCreditFreeze{}, f.record(a, []string{hold, reason, fmt.Sprint(noFault)})
}

func (f *consoleWritesFake) ConsoleDeleteAccount(_ context.Context, a capability.Authorized) (store.AccountDeletion, error) {
	return store.AccountDeletion{AccountID: a.Target.AccountID}, f.record(a, a.Target.AccountID)
}

func (f *consoleWritesFake) ConsoleDeleteTeam(_ context.Context, a capability.Authorized) (store.TeamDeletion, error) {
	return store.TeamDeletion{Team: store.Team(a.Target.Team)}, f.record(a, a.Target.Team)
}

func (f *consoleWritesFake) ConsoleApproveWaitlist(_ context.Context, a capability.Authorized, ids []string, oldest int, key string) ([]store.Account, error) {
	return nil, f.record(a, consoleApprovalInput{ids, oldest, key})
}

func (f *consoleWritesFake) ConsoleSetCreditSettings(_ context.Context, a capability.Authorized, value store.CreditSettingsUpdate) (store.CreditSettings, error) {
	return store.CreditSettings{}, f.record(a, value)
}

func (f *consoleWritesFake) ConsoleUnbindGitHubInstallation(_ context.Context, a capability.Authorized, id int64) (store.Team, error) {
	return "customer", f.record(a, id)
}

func TestConsoleWriteHTTPAuthorityProperty(t *testing.T) {
	s, _, authority := newOperatorSettingsFixture(t)
	s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	base := authority.principal
	base.Class = capability.CredentialConsole
	base.Authority = capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(base.Capability.Digest[:])}
	base.Capability.Prefix = ""
	base.Capability.Grants = []capability.Grant{capability.OperatorConsole}
	s.WithAuthorization(RouteAuthorization{
		Authenticator: capability.NewAuthenticator(authority), Bindings: authority,
		Identify: func(*http.Request) (capability.Presentation, error) {
			return capability.Presentation{Class: authority.principal.Class, Raw: authority.raw}, nil
		},
		CheckCSRF: func(r *http.Request, _ capability.Principal) error {
			if r.Header.Get(csrfHeaderName) != "session-proof" {
				return &apierr.Error{Code: apierr.Forbidden, Message: "invalid CSRF token"}
			}
			return nil
		},
	})
	s.githubApp = &githubAppState{}
	f := &consoleWritesFake{}
	s.consoleWrites = f
	cases := []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/operator/teams/customer/refunds", `{"payment_id":"pi_test","refund_id":"re_test","amount_cents":10,"reason":"support"}`, 200},
		{"POST", "/api/v1/operator/teams/customer/grants", `{"amount_cents":100,"reason":"promotion","key":"once"}`, 201},
		{"POST", "/api/v1/operator/teams/customer/trust", `{"trust":"granted","reason":"review"}`, 200},
		{"POST", "/api/v1/operator/teams/customer/freeze", `{"reason":"review"}`, 200},
		{"POST", "/api/v1/operator/teams/customer/standing-request/decline", `{"request":"standing-1","reason":"review"}`, 200},
		{"POST", "/api/v1/operator/teams/customer/unfreeze", `{"reason":"resolved","hold":"dispute"}`, 200},
		{"DELETE", "/api/v1/operator/teams/customer", ``, 202},
		{"DELETE", "/api/v1/operator/accounts/target@example.test", ``, 200},
		{"POST", "/api/v1/operator/waitlist/approve", `{"account_ids":["waiting"]}`, 200},
		{"PUT", "/api/v1/operator/credits/settings", `{"warm_cpu_class_cores":1}`, 200},
		{"DELETE", "/api/v1/operator/github-app/installations/17", ``, 204},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			property := func(input []byte, phase uint8) bool {
				authority.principal = base
				f.calls, f.err = 0, nil
				s.consoleWrites = f
				want := tc.status
				r := httptest.NewRequest(tc.method, tc.path+"?team=foreign&actor="+hex.EncodeToString(input), strings.NewReader(tc.body))
				r.Header.Set(csrfHeaderName, "session-proof")
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Idempotency-Key", "approve-once")
				switch phase % 6 {
				case 1:
					authority.principal.Class = capability.CredentialSession
					authority.principal.Capability.Subject.Kind = capability.SubjectAccount
					want = 403
				case 2:
					r.Header.Del(csrfHeaderName)
					want = 403
				case 3:
					authority.principal.Capability.Grants = nil
					want = 403
				case 4:
					f.err = &apierr.Error{Code: apierr.Forbidden, Message: "operator session changed"}
					want = 403
				case 5:
					f.err = errors.New("private-backend-fault")
					want = 500
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				writes := phase%6 == 0 || phase%6 == 4 || phase%6 == 5
				if w.Code != want || (f.calls == 1) != writes || f.calls > 1 || strings.Contains(w.Body.String(), "private-backend-fault") {
					t.Logf("phase %d = %d %s calls=%d want=%d", phase%6, w.Code, w.Body, f.calls, want)
					return false
				}
				if writes && (f.seen.Principal.Authority != base.Authority || f.seen.Principal.Capability.Subject.ID != "operator-account" || f.seen.Target.Team != "customer" && strings.Contains(tc.path, "/teams/")) {
					return false
				}
				if writes && strings.HasSuffix(tc.path, "/waitlist/approve") {
					input, ok := f.value.(consoleApprovalInput)
					if !ok || input.Key != "approve-once" || len(input.IDs) != 1 || input.IDs[0] != "waiting" || input.Oldest != 0 {
						return false
					}
				}
				if want >= 400 {
					var body apierr.Envelope
					if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error == nil {
						return false
					}
				}
				return sha256.Sum256([]byte(authority.raw)) == base.Capability.Digest
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (f *consoleWritesFake) ConsoleRefund(_ context.Context, a capability.Authorized, req controllerwire.OperatorRefund, key string) (store.CreditReversal, error) {
	return store.CreditReversal{}, f.record(a, req)
}
