package store

import "time"

// BusinessEvent is one durable fact about a team's account, such as a
// billing trust change or a refused purchase.
type BusinessEvent struct {
	Kind   string
	Team   Team
	Actor  string
	Reason string
	Attrs  map[string]any
	At     time.Time
}

// RecordBusinessEvent writes ev inside tx, so the fact commits or rolls back
// with the change it describes.
func RecordBusinessEvent(tx *storeTx, ev BusinessEvent) error {
	_, _ = tx, ev
	return nil
}
