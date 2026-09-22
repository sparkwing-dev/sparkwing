package controller

import (
	"testing"
)

// TeamBoundaryExempt exposes the routes the team boundary leaves to their own
// gate, so the external boundary test can hold every other run route to it.
var TeamBoundaryExempt = teamBoundaryExempt

// MuxRouteScopes returns every route server.go registers on the authenticated
// mux with the scope requireScope gates it on, read from the source so a route
// added later is in the list without anyone adding it.
func MuxRouteScopes(t *testing.T) map[string]string {
	t.Helper()
	return muxRoutes(t, "server.go")
}
