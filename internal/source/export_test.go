package source

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// SetLookupHost replaces the clone host resolver for one test.
func SetLookupHost(t *testing.T, lookup sourceurl.Lookup) {
	previous := lookupHost
	lookupHost = lookup
	t.Cleanup(func() { lookupHost = previous })
}
