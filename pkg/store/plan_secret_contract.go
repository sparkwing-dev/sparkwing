package store

import "context"

// PlanSecretOpener decrypts a stored envelope without releasing its plaintext to a host.
// The store selects the tenant and pipeline scope inside the acceptance transaction.
type PlanSecretOpener interface {
	OpenPlanSecret(context.Context, Team, Secret) (string, error)
}

// SetPlanSecretOpener configures server-side identifier validation before the store is shared.
// Acceptance refuses encrypted secret validation when no opener is configured.
func (s *Store) SetPlanSecretOpener(opener PlanSecretOpener) {
	panic("unimplemented: c1-data")
}
