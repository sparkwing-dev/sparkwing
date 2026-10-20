package acceptance_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
)

type runnerMeteringWriter func(context.Context, capability.Authorized, string, bool) error

func (f runnerMeteringWriter) SetRunnerMetered(ctx context.Context, a capability.Authorized, runner string, metered bool) error {
	return f(ctx, a, runner, metered)
}

func TestRunnerMeteringHTTPConsoleProperty(t *testing.T) {
	b := consoleBoundary()
	var calls int
	var actor capability.Authorized
	var selected string
	var marked bool
	s := licensedCreditServer(t, b).WithRunnerMeteringWriter(runnerMeteringWriter(func(_ context.Context, a capability.Authorized, runner string, metered bool) error {
		calls++
		actor, selected, marked = a, runner, metered
		return nil
	}))
	h := s.Handler()
	property := func(selector string, metered bool) bool {
		before := calls
		body := `{"metered":false}`
		if metered {
			body = `{"metered":true}`
		}
		w := authorityHTTP(h, b, "POST", "/api/v1/operator/runners/swk_ABCDEFGHIJKL/metered?"+url.Values{"team": {selector}, "runner": {selector}}.Encode(), body)
		return w.Code == 200 && calls == before+1 && selected == "swk_ABCDEFGHIJKL" && marked == metered && actor.Principal.Authority == b.snapshot.Authorities[0].Principal.Authority && actor.Target.Team == "" && w.Header().Get("Cache-Control") == "no-store"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerMeteringHTTPRefusals(t *testing.T) {
	for _, name := range []string{"bearer", "member", "peer", "revoked", "csrf", "unlicensed", "writer", "private", "race", "unknown runner", "absent", "null", "wrong type", "unknown field"} {
		t.Run(name, func(t *testing.T) {
			b, calls := consoleBoundary(), 0
			want, body := apierr.Forbidden, `{"metered":true}`
			var failure error
			switch name {
			case "bearer":
				b = authorityMember()
			case "member":
				b = authorityMember()
				p := &b.snapshot.Authorities[0].Principal
				p.Class, p.Authority = capability.CredentialSession, sessionReference(*p)
			case "peer":
				b = authorityMember()
				p := &b.snapshot.Authorities[0].Principal
				p.Class, p.Authority, p.Capability.Subject = capability.CredentialPeer, capability.AuthorityRef{}, capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt, want = &b.snapshot.Now, apierr.Unauthenticated
			case "unlicensed":
				want = apierr.NotFound
			case "writer":
				want = apierr.PolicyMissing
			case "private":
				failure, want = errors.New("private database credential"), apierr.Internal
			case "race":
				failure, want = &apierr.Error{Code: apierr.LockHeld, Message: "authority changed"}, apierr.LockHeld
			case "unknown runner":
				failure, want = &apierr.Error{Code: apierr.NotFound, Message: "runner not found"}, apierr.NotFound
			case "absent":
				body, want = `{}`, apierr.BadRequest
			case "null":
				body, want = `{"metered":null}`, apierr.BadRequest
			case "wrong type":
				body, want = `{"metered":"true"}`, apierr.BadRequest
			case "unknown field":
				body, want = `{"metered":true,"team":"forged"}`, apierr.BadRequest
			}
			s := licensedCreditServer(t, b)
			if name == "csrf" {
				a := consoleAuthorization(b)
				a.CheckCSRF = nil
				s.WithAuthorization(a)
				want = apierr.PolicyMissing
			}
			if name == "unlicensed" {
				s = controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
			}
			if name != "writer" {
				s.WithRunnerMeteringWriter(runnerMeteringWriter(func(context.Context, capability.Authorized, string, bool) error { calls++; return failure }))
			}
			w := authorityHTTP(s.Handler(), b, "POST", "/api/v1/operator/runners/swk_ABCDEFGHIJKL/metered", body)
			assertEngineError(t, w, apierr.Status(want), want)
			expected := 0
			if failure != nil {
				expected = 1
			}
			if calls != expected || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("HTTP %d %s writes=%d", w.Code, w.Body.String(), calls)
			}
		})
	}
}
