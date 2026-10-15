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
	Verify         func(*http.Request) error
	Handle         func(http.ResponseWriter, *http.Request, capability.Authorized) error
}

// PolicyRouter matches a route before authentication and enforces its complete policy.
// Register is a startup operation; registrations are immutable while serving requests.
type PolicyRouter struct{}

// NewPolicyRouter constructs the common controller/node-host authorization boundary.
// Registration inventories come from handlers independently of capability.Routes.
func NewPolicyRouter(authorization RouteAuthorization) *PolicyRouter {
	panic("unimplemented: c2-credentials")
}

// Register rejects duplicate or conflicting handlers. Missing policy remains a
// runtime policy_missing refusal and a validation failure, never an exposed handler.
func (router *PolicyRouter) Register(endpoint Endpoint) error {
	panic("unimplemented: c2-credentials")
}

// Registrations returns actual method/pattern keys for independent inventory checks.
func (router *PolicyRouter) Registrations() []string {
	panic("unimplemented: c2-credentials")
}

// Validate checks handler proof wiring, including CSRF for session/console mutations.
// Complete controller and node-host inventories are combined for ValidateRoutes.
func (router *PolicyRouter) Validate() error {
	panic("unimplemented: c2-credentials")
}

// ServeHTTP applies authentication before authorization and uses the shared error
// object for failures, including genuinely absent routes and unsupported methods.
func (router *PolicyRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	panic("unimplemented: c2-credentials")
}

var _ http.Handler = (*PolicyRouter)(nil)
