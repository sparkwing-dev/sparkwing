package controller

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// RouteAuthorization supplies the same authority checks for HTTP and verified local transports.
// Identify handles sessions and local peers only when no Authorization header is present.
// A peer identity must come from OS credentials, never an address or a request header.
type RouteAuthorization struct {
	Authenticator capability.Authenticator
	Bindings      capability.BindingChecker
	Identify      func(*http.Request) (capability.Presentation, error)
	CheckCSRF     func(*http.Request, capability.Principal) error
}

// Endpoint binds a handler and its decoder to one router registration. Decode must
// resolve ownership and body selectors, preserving the body for Handle when needed.
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
// Register is a startup operation. The router is immutable while serving requests.
type PolicyRouter struct {
	mux           *http.ServeMux
	authorization RouteAuthorization
	registered    map[string]Endpoint
}

// NewPolicyRouter constructs the common controller/node-host authorization boundary.
// Registration inventories come from handlers, independently of capability.Routes.
func NewPolicyRouter(authorization RouteAuthorization) *PolicyRouter {
	return &PolicyRouter{mux: http.NewServeMux(), authorization: authorization, registered: make(map[string]Endpoint)}
}

// Register rejects duplicate or conflicting handlers. Missing policy remains a runtime
// policy_missing refusal as well as a Validate failure, so it cannot expose a handler.
func (router *PolicyRouter) Register(endpoint Endpoint) (err error) {
	key := endpoint.Method + " " + endpoint.Pattern
	if endpoint.Handle == nil || endpoint.Method == "" || !strings.HasPrefix(endpoint.Pattern, "/") {
		return errors.New("incomplete endpoint registration")
	}
	if _, exists := router.registered[key]; exists {
		return fmt.Errorf("duplicate endpoint: %s", key)
	}
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("invalid endpoint %s: %v", key, failure)
		}
	}()
	router.mux.HandleFunc(key, func(w http.ResponseWriter, r *http.Request) {
		// safety: ServeMux lets GET match HEAD, but grants apply to exact methods.
		if r.Method != endpoint.Method {
			w.Header().Set("Allow", endpoint.Method)
			writePolicyError(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "method is not registered"})
			return
		}
		if err := router.serveEndpoint(w, r, endpoint); err != nil {
			writePolicyError(w, err)
		}
	})
	router.registered[key] = endpoint
	return nil
}

// Registrations returns actual method/pattern keys for the independent route inventory check.
func (router *PolicyRouter) Registrations() []string {
	keys := make([]string, 0, len(router.registered))
	for key := range router.registered {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// Validate checks registered handlers and their proof wiring. The complete controller
// and node-host inventories must be combined and checked with capability.ValidateRoutes.
func (router *PolicyRouter) Validate() error {
	for _, endpoint := range router.registered {
		route, found := capability.LookupRoute(endpoint.Method, endpoint.Pattern)
		if !found {
			return fmt.Errorf("endpoint %s %s: %w", endpoint.Method, endpoint.Pattern, missingEndpointPolicy())
		}
		if err := router.checkEndpoint(route, endpoint); err != nil {
			return fmt.Errorf("endpoint %s %s: %w", endpoint.Method, endpoint.Pattern, err)
		}
	}
	return nil
}

func (router *PolicyRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := router.mux.Handler(r); pattern == "" {
		allowed := router.allowedMethods(r)
		if len(allowed) != 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writePolicyError(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "method is not registered"})
		} else {
			writePolicyError(w, &apierr.Error{Code: apierr.NotFound, Message: "route not found"})
		}
		return
	}
	router.mux.ServeHTTP(w, r)
}

func (router *PolicyRouter) allowedMethods(r *http.Request) []string {
	var methods []string
	seen := make(map[string]bool)
	probe := r.Clone(r.Context())
	for _, endpoint := range router.registered {
		if seen[endpoint.Method] {
			continue
		}
		seen[endpoint.Method] = true
		probe.Method = endpoint.Method
		if _, pattern := router.mux.Handler(probe); pattern != "" {
			methods = append(methods, probe.Method)
		}
	}
	slices.Sort(methods)
	return methods
}

func (router *PolicyRouter) checkEndpoint(route capability.RoutePolicy, endpoint Endpoint) error {
	if route.Authentication != endpoint.Authentication {
		return missingEndpointPolicy()
	}
	if route.Authentication == capability.AuthCapability {
		if endpoint.Decode == nil || endpoint.Verify != nil || router.authorization.Authenticator == nil || router.authorization.Bindings == nil {
			return missingEndpointPolicy()
		}
	} else if route.Authentication != capability.AuthPublic && endpoint.Verify == nil {
		return missingEndpointPolicy()
	}
	return nil
}

func (router *PolicyRouter) serveEndpoint(w http.ResponseWriter, r *http.Request, endpoint Endpoint) error {
	route, found := capability.LookupRoute(endpoint.Method, endpoint.Pattern)
	if !found {
		return missingEndpointPolicy()
	}
	if err := router.checkEndpoint(route, endpoint); err != nil {
		return err
	}
	if route.Authentication != capability.AuthCapability {
		if endpoint.Verify != nil {
			if err := endpoint.Verify(r); err != nil {
				return err
			}
		}
		return endpoint.Handle(w, r, capability.Authorized{})
	}
	presentation, err := router.presentation(r)
	if err != nil {
		return err
	}
	presentation.Evidence, err = endpoint.Decode(r)
	decodeErr := err
	authenticated, err := router.authorization.Authenticator.Authenticate(r.Context(), presentation, route)
	if err != nil {
		return err
	}
	// safety: unknown or dead credentials cannot use decoder failures as an existence oracle.
	if decodeErr != nil {
		return decodeErr
	}
	if requiresCSRF(r.Method, authenticated.Principal.Class) {
		if router.authorization.CheckCSRF == nil {
			return missingEndpointPolicy()
		}
		if err := router.authorization.CheckCSRF(r, authenticated.Principal); err != nil {
			return err
		}
	}
	authorized, err := capability.Authorize(r.Context(), router.authorization.Bindings, route, authenticated, presentation.Evidence.Target)
	if err != nil {
		return err
	}
	return endpoint.Handle(w, r, authorized)
}

func (router *PolicyRouter) presentation(r *http.Request) (capability.Presentation, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 0 {
		if len(values) == 1 {
			fields := strings.Fields(values[0])
			if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
				return capability.Presentation{Raw: fields[1], Class: capability.CredentialBearer}, nil
			}
		}
		return capability.Presentation{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "invalid credential presentation"}
	}
	if router.authorization.Identify != nil {
		return router.authorization.Identify(r)
	}
	return capability.Presentation{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "credential required"}
}

func requiresCSRF(method string, class capability.CredentialClass) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions &&
		(class == capability.CredentialSession || class == capability.CredentialConsole)
}

func missingEndpointPolicy() *apierr.Error {
	return &apierr.Error{Code: apierr.PolicyMissing, Message: "route authorization policy is incomplete"}
}

func writePolicyError(w http.ResponseWriter, err error) {
	var failure *apierr.Error
	if !errors.As(err, &failure) {
		failure = &apierr.Error{Code: apierr.Internal, Message: "request failed"}
	}
	var encoded bytes.Buffer
	if err := apierr.WriteJSON(&encoded, failure); err != nil {
		failure = &apierr.Error{Code: apierr.Internal, Message: "request failed"}
	}
	if err := apierr.WriteHTTP(w, failure); err != nil {
		// safety: internal errors may contain credentials; log only the safe code.
		slog.Error("write API error response", "code", failure.Code)
	}
}
