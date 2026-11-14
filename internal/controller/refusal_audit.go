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

const (
	// safety: retention keeps rows for 400 days, so a team with many runner and
	// node credentials is capped per window; the rest are counted in one summary row.
	refusalTeamKeys  = 300
	refusalWarnEvery = time.Minute
	refusalTimeout   = 5 * time.Second
)

type refusalKey struct {
	subject   capability.Subject
	authority capability.AuthorityRef
	holder    string
	unit      capability.UnitKind
	class     capability.CredentialClass
	method    string
	pattern   string
	refusal   routepolicy.RefusalClass
}

type refusalTeam struct {
	keys       map[refusalKey]struct{}
	suppressed int64
}

// perf: refused requests are answered before any request budget applies, so a
// key already written this window skips its write transaction, and the whole
// map resets when the window rolls.
type refusalAudit struct {
	record   func(context.Context, store.AccessRefusal) error
	suppress func(context.Context, store.AccessSuppression) error
	now      func() time.Time
	logger   *slog.Logger

	mu       sync.Mutex
	window   time.Time
	teams    map[string]*refusalTeam
	warned   time.Time
	failures int64
}

func newRefusalAudit(op *store.Operator, logger *slog.Logger) *refusalAudit {
	return &refusalAudit{record: op.RecordAccessRefusal, suppress: op.RecordAccessSuppression, now: time.Now, logger: logger}
}

func (a *refusalAudit) note(ctx context.Context, refusal routepolicy.Refusal) {
	p := refusal.Principal
	team := p.Capability.Team
	// safety: only a team's operator view reads these rows, so a team-less principal's row could never be read.
	if team == "" {
		return
	}
	at := a.now()
	key := refusalKey{
		subject: p.Capability.Subject, authority: p.Authority, holder: p.Capability.Binding.Holder,
		unit: p.Capability.Unit, class: p.Class, method: refusal.Method, pattern: refusal.Pattern, refusal: refusal.Class,
	}
	write, summaries := a.admit(team, key, at)
	if !write && len(summaries) == 0 {
		return
	}
	// safety: a prober that hangs up at once must still leave its row.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refusalTimeout)
	defer cancel()
	for _, summary := range summaries {
		if err := a.suppress(ctx, summary); err != nil {
			a.warn(ctx, at, err)
		}
	}
	if !write {
		return
	}
	if err := a.record(ctx, store.AccessRefusal{At: at, Principal: p, Method: refusal.Method, Route: refusal.Pattern, Refusal: string(refusal.Class)}); err != nil {
		a.forget(team, key)
		a.warn(ctx, at, err)
	}
}

// safety: no timer runs, so the first refusal after a window rolls carries that
// window's suppression summaries out.
func (a *refusalAudit) admit(team string, key refusalKey, at time.Time) (bool, []store.AccessSuppression) {
	window := at.Truncate(store.AccessRefusalWindow)
	a.mu.Lock()
	defer a.mu.Unlock()
	var summaries []store.AccessSuppression
	if !window.Equal(a.window) {
		for name, t := range a.teams {
			if t.suppressed > 0 {
				summaries = append(summaries, store.AccessSuppression{At: at, Team: store.Team(name), Window: a.window, Suppressed: t.suppressed})
			}
		}
		a.window, a.teams = window, make(map[string]*refusalTeam)
	}
	t := a.teams[team]
	if t == nil {
		t = &refusalTeam{keys: make(map[refusalKey]struct{})}
		a.teams[team] = t
	}
	if _, ok := t.keys[key]; ok {
		return false, summaries
	}
	if len(t.keys) >= refusalTeamKeys {
		t.suppressed++
		return false, summaries
	}
	t.keys[key] = struct{}{}
	return true, summaries
}

func (a *refusalAudit) forget(team string, key refusalKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t := a.teams[team]; t != nil {
		delete(t.keys, key)
	}
}

func (a *refusalAudit) warn(ctx context.Context, at time.Time, err error) {
	a.mu.Lock()
	a.failures++
	if !a.warned.IsZero() && at.Sub(a.warned) < refusalWarnEvery {
		a.mu.Unlock()
		return
	}
	failures := a.failures
	a.warned, a.failures = at, 0
	a.mu.Unlock()
	a.logger.WarnContext(ctx, "access refusal audit failed", "failures", failures, "err", err)
}
