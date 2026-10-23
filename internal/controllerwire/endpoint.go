package controllerwire

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Endpoint binds a handler and decoder to one actual router registration.
type Endpoint struct {
	Method         string
	Pattern        string
	Authentication capability.Authentication
	Decode         func(*http.Request) (capability.RequestEvidence, error)
	ResolveTarget  func(context.Context, capability.Principal, capability.Target) (capability.Target, error)
	Verify         func(*http.Request) error
	Handle         func(http.ResponseWriter, *http.Request, capability.Authorized) error
}
