package controller_test

import "github.com/sparkwing-dev/sparkwing/internal/store"

var (
	machineScopes = []string{"nodes.claim", "triggers.claim"}
	memberScopes  = store.ScopesForRole(store.RoleEditor)
)
