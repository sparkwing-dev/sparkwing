package controller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// perf: past this many keys a window stops remembering them; the store's
// deterministic row id still keeps one row per key.
const maxRefusalKeys = 4096

const refusalAuditTimeout = 5 * time.Second

type refusalKey struct {
	team      string
	subject   capability.Subject
	authority capability.AuthorityRef
	holder    string
	unit      capability.UnitKind
	class     capability.CredentialClass
	method    string
	pattern   string
	refusal   routepolicy.RefusalClass
}

// perf: refused requests are answered before any request budget applies, so a
// key already written this window skips its write transaction.
type refusalAudit struct {
	record func(context.Context, store.AccessRefusal) error
	now    func() time.Time
	logger *slog.Logger

	mu     sync.Mutex
	window time.Time
	seen   map[refusalKey]struct{}
}

func newRefusalAudit(op *store.Operator, logger *slog.Logger) *refusalAudit {
	return &refusalAudit{record: op.RecordAccessRefusal, now: time.Now, logger: logger}
}

func (a *refusalAudit) note(ctx context.Context, refusal routepolicy.Refusal) {
	at := a.now()
	p := refusal.Principal
	key := refusalKey{
		team: p.Capability.Team, subject: p.Capability.Subject, authority: p.Authority, holder: p.Capability.Binding.Holder,
		unit: p.Capability.Unit, class: p.Class, method: refusal.Method, pattern: refusal.Pattern, refusal: refusal.Class,
	}
	if !a.first(key, at.Truncate(store.AccessRefusalWindow)) {
		return
	}
	// safety: a prober that hangs up at once must still leave its row.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refusalAuditTimeout)
	defer cancel()
	err := a.record(ctx, store.AccessRefusal{At: at, Principal: p, Method: refusal.Method, Route: refusal.Pattern, Refusal: string(refusal.Class)})
	if err != nil {
		a.forget(key)
		a.logger.WarnContext(ctx, "access refusal audit failed", "route", refusal.Pattern, "refusal", string(refusal.Class), "err", err)
	}
}

func (a *refusalAudit) first(key refusalKey, window time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !window.Equal(a.window) {
		a.window, a.seen = window, make(map[refusalKey]struct{})
	}
	if _, ok := a.seen[key]; ok {
		return false
	}
	if len(a.seen) < maxRefusalKeys {
		a.seen[key] = struct{}{}
	}
	return true
}

func (a *refusalAudit) forget(key refusalKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.seen, key)
}
