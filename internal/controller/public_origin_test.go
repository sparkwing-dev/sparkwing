package controller

import (
	"errors"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestPublicOriginRequiresDeclaredExternalURL(t *testing.T) {
	for _, origin := range []string{"", "https://ci.example.com"} {
		t.Run(origin, func(t *testing.T) {
			s := &Server{deployment: deploymenttest.Local(t, origin)}
			got, err := s.publicOrigin()
			if origin != "" {
				if err != nil || got != origin {
					t.Fatalf("public origin = %q, %v; want %q", got, err, origin)
				}
				return
			}
			var refusal *apierr.Error
			if got != "" || !errors.As(err, &refusal) || refusal.Code != hosterr.CapabilityUnsupported {
				t.Fatalf("undeclared origin = %q, %v; want capability_unsupported", got, err)
			}
		})
	}
}
