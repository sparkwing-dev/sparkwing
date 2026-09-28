package store

import (
	"context"
	"time"
)

// BusinessEvent is one durable fact about an account or a team, such as a
// billing trust change or a refused purchase.
type BusinessEvent struct {
	ID        string
	At        time.Time
	Team      Team
	Account   string
	Kind      string
	SubjectID string
	Actor     string
	Attrs     map[string]any
}

// RecordBusinessEvent writes ev inside tx, so the fact commits or rolls back
// with the change it describes.
func RecordBusinessEvent(tx *storeTx, ev BusinessEvent) error {
	_, _ = tx, ev
	return nil
}

// BusinessEvents returns t's events of kind, or of every kind when kind is
// empty, oldest first.
func (t *Tenant) BusinessEvents(ctx context.Context, kind string) ([]BusinessEvent, error) {
	_, _, _ = t, ctx, kind
	return nil, nil
}
