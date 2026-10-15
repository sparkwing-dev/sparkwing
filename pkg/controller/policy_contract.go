package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// RouteAuthorization supplies the same checks for HTTP and verified local transports.
// Identify handles sessions and local peers only when no Authorization header exists.
// Peer identity comes from OS credentials, never an address or request header.
type RouteAuthorization struct {
	Authenticator capability.Authenticator
	Bindings      capability.BindingChecker
	Identify      func(*http.Request) (capability.Presentation, error)
	CheckCSRF     func(*http.Request, capability.Principal) error
}

// Endpoint binds a handler and decoder to one actual router registration.
// Decode resolves ownership and body selectors, preserving the exact body when needed.
// Decoder errors are returned only after authentication to avoid existence disclosure.
// Verify authenticates the declared external proof before any external handler runs.
// Handle returns errors before writing its response; store writes recheck Authorized.
type Endpoint struct {
	Method         string
	Pattern        string
	Authentication capability.Authentication
	Decode         func(*http.Request) (capability.RequestEvidence, error)
	// ResolveTarget derives implicit resource selectors from the authenticated principal.
	ResolveTarget func(capability.Principal, capability.Target) (capability.Target, error)
	Verify        func(*http.Request) error
	Handle        func(http.ResponseWriter, *http.Request, capability.Authorized) error
}

// PolicyRouter matches a route before authentication and enforces its complete policy.
// Register is a startup operation; registrations are immutable while serving requests.
type PolicyRouter struct {
	mux           *http.ServeMux
	authorization RouteAuthorization
	registered    map[string]Endpoint
}
