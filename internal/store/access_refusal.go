package store

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// AccessRefusalWindow is the span within which repeated refusals of one
// credential on one route and class share a single access.refused row.
const AccessRefusalWindow = time.Hour

// AccessRefusal is an authenticated request refused for the resource it named.
// Route is the registered pattern; Refusal is not_found, team or binding.
type AccessRefusal struct {
	At        time.Time
	Principal capability.Principal
	Method    string
	Route     string
	Refusal   string
}

// RecordAccessRefusal audits a refusal as access.refused in the acting
// principal's team, keeping at most one row per actor, route, class and
// credential kind in each AccessRefusalWindow.
func (o *Operator) RecordAccessRefusal(ctx context.Context, r AccessRefusal) error {
	if r.At.IsZero() {
		return fmt.Errorf("%w: refusal time is required", ErrInvalidInput)
	}
	payload := auditAccessRefusal{
		Window: r.At.Truncate(AccessRefusalWindow).Unix(), Method: r.Method, Route: r.Route, Refusal: r.Refusal,
		Credential: refusalCredential(r.Principal), Presentation: string(r.Principal.Class),
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		return o.recordAudit(ctx, tx, auditContext{At: r.At, Team: Team(r.Principal.Capability.Team), Actor: auditPrincipalActor(r.Principal)}, payload)
	})
}

func refusalCredential(p capability.Principal) string {
	c := p.Capability
	switch {
	case c.Unit != "":
		return string(c.Unit)
	case c.Subject.Kind == capability.SubjectMachine:
		return "runner"
	default:
		return string(c.Subject.Kind)
	}
}
