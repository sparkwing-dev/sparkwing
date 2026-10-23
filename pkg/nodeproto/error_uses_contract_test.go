package nodeproto

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestProtocolErrorUsesCanonicalRegistry(t *testing.T) {
	registered := make(map[apierr.Code]bool, len(apierr.Codes))
	for _, definition := range apierr.Codes {
		registered[definition.Code] = true
	}
	for _, use := range ErrorUses() {
		if !registered[use.Code] {
			t.Errorf("%s uses unregistered code %q", use.Surface, use.Code)
		}
		if use.Code == apierr.Forbidden && use.Reason == "" {
			t.Errorf("%s omits its forbidden reason", use.Surface)
		}
	}
}
