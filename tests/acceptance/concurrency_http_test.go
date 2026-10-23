package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type concurrencyBoundary struct {
	*credentialBoundary
	group string
}

func (b concurrencyBoundary) CheckBinding(ctx context.Context, p capability.Principal, rule capability.BindingRule, target capability.Target) error {
	if err := b.credentialBoundary.CheckBinding(ctx, p, rule, target); err != nil {
		return err
	}
	if rule == capability.BindSlotOfBoundNode && (target.SlotKey != b.group) {
		return &apierr.Error{Code: apierr.Forbidden, Message: "undeclared slot", Details: map[string]any{"reason": apierr.ReasonBinding}}
	}
	return nil
}

type concurrencyTransactions struct {
	team  store.Team
	calls int
	auth  capability.Authorized
	err   error
	group string
}

func (f *concurrencyTransactions) Team() store.Team { return f.team }
func (f *concurrencyTransactions) entered(a capability.Authorized) error {
	f.calls++
	f.auth = a
	return f.err
}

func (f *concurrencyTransactions) ReadAuthorizedGroup(_ context.Context, a capability.Authorized, group string) (*store.ConcurrencyState, error) {
	if f.group != "" {
		group = f.group
	}
	return &store.ConcurrencyState{
		Key: group, Capacity: 2, EffectiveCapacity: 1, UsedCost: 1,
		Holders: []store.ConcurrencyHolder{{HolderID: "other-holder", RunID: "run", NodeID: "sibling", Superseded: true, Cost: 1, ClaimedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), LeaseExpiresAt: time.Date(2026, 10, 14, 12, 1, 0, 0, time.UTC)}},
		Waiters: []store.ConcurrencyWaiter{{RunID: "next", NodeID: "next-job", Position: 0, Policy: "queue", ArrivedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}},
	}, f.entered(a)
}

func concurrencyServer(b *credentialBoundary, f *concurrencyTransactions, group string) http.Handler {
	auth := consoleAuthorization(b)
	auth.Bindings = concurrencyBoundary{b, group}
	return bindTenantPort[controller.ConcurrencyStore](controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(auth),
		func(_ context.Context, _ store.Team) (controller.ConcurrencyStore, error) { return f, nil }).Handler()
}

func concurrencyRequest(h http.Handler, b *credentialBoundary, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if b.raw != "" {
		r.Header.Set("Authorization", "Bearer "+b.raw)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Sparkwing-Generation", "999")
	r.Header.Set("X-Sparkwing-Holder", "foreign")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestConcurrencyHTTPRetiredRoutes(t *testing.T) {
	b := newCredentialBoundary()
	f := &concurrencyTransactions{team: "team"}
	h := concurrencyServer(b, f, "deploy")
	for _, base := range []string{"/api/v1/concurrency/", "/api/v1/queue/groups/"} {
		for _, method := range []string{"GET", "POST"} {
			for _, op := range []string{"acquire", "heartbeat", "release", "holder", "state", "resolve", "cancel-waiter", "force-release"} {
				if w := concurrencyRequest(h, b, method, base+"deploy/"+op, `{}`); w.Code != 404 || f.calls != 0 {
					t.Fatalf("retired %s %s HTTP %d %s", method, op, w.Code, w.Body)
				}
			}
		}
	}
}

var _ controller.ConcurrencyStore = (*concurrencyTransactions)(nil)

func TestConcurrencyHTTPGroupRead(t *testing.T) {
	for _, owner := range []string{"job", "trigger", "plan"} {
		t.Run(owner, func(t *testing.T) {
			b := newCredentialBoundary()
			if owner == "trigger" {
				b = ownerBoundary()
			}
			if owner == "plan" {
				b = planBoundary()
				b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.RunsState)
			}
			f := &concurrencyTransactions{team: "team"}
			h := concurrencyServer(b, f, "deploy")
			w := concurrencyRequest(h, b, "GET", "/api/v1/queue/groups/deploy", "")
			var result controllerwire.QueueGroup
			if w.Code != 200 || f.calls != 1 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Group != "deploy" || len(result.Waiters) != 1 || result.Waiters[0].Position != 1 || result.Holders[0].JobID != "sibling" || !result.Holders[0].Superseded {
				t.Fatalf("group HTTP %d %s calls=%d", w.Code, w.Body, f.calls)
			}
			w = concurrencyRequest(h, b, "GET", "/api/v1/queue/groups/foreign", "")
			if w.Code != 403 || f.calls != 1 {
				t.Fatalf("foreign group HTTP %d %s calls=%d", w.Code, w.Body, f.calls)
			}
		})
	}
}
