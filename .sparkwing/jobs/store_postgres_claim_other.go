//go:build !unix

package jobs

import "context"

const storePostgresOwnerLock = "owner.lock"

// hack: no flock outside unix, so a root a killed run left behind stays until
// the operator removes it.
func claimStorePostgresRoot(string) (func(), error) { return func() {}, nil }

func sweepAbandonedStorePostgresRoots(context.Context, string) {}
