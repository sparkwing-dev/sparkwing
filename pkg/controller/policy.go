package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// RouteAuthorization supplies the same checks for HTTP and verified local transports.
type RouteAuthorization struct {
	Authenticator capability.Authenticator
	Bindings      capability.BindingChecker
	Identify      func(*http.Request) (capability.Presentation, error)
	CheckCSRF     func(*http.Request, capability.Principal) error
}

// Endpoint binds a handler and decoder to one actual router registration.
type Endpoint struct {
	Method         string
	Pattern        string
	Authentication capability.Authentication
	Decode         func(*http.Request) (capability.RequestEvidence, error)
	ResolveTarget  func(capability.Principal, capability.Target) (capability.Target, error)
	Verify         func(*http.Request) error
	Handle         func(http.ResponseWriter, *http.Request, capability.Authorized) error
}

// PolicyRouter matches a route before authentication and enforces its complete policy.
type PolicyRouter struct {
	mux           *http.ServeMux
	authorization RouteAuthorization
	registered    map[string]Endpoint
}
