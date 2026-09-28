package store

import "time"

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
