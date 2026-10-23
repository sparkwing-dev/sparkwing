package routepolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// New constructs the common controller/node-host authorization boundary.
// Registration inventories come from handlers, independently of capability.Routes().
func New(authorization Authorization) *Router {
	return &Router{Mux: http.NewServeMux(), authorization: authorization, registered: make(map[string]controllerwire.Endpoint)}
}

// Register rejects duplicate or conflicting handlers. Missing policy remains a runtime
// policy_missing refusal as well as a Validate failure, so it cannot expose a handler.
func (router *Router) Register(endpoint controllerwire.Endpoint) (err error) {
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
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(router.context(r.Context()))
		// safety: ServeMux lets GET match HEAD, but grants apply to exact methods.
		if r.Method != endpoint.Method {
			w.Header().Set("Allow", endpoint.Method)
			WritePolicyError(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "method is not registered"})
			return
		}
		if err := router.serveEndpoint(w, r, endpoint); err != nil {
			WritePolicyError(w, err)
		}
	})
	var protected http.Handler = handler
	if endpoint.Authentication != capability.AuthPublic {
		protected = noStore(protected)
	}
	router.Mux.Handle(key, protected)
	router.registered[key] = endpoint
	return nil
}

// Registrations returns actual method/pattern keys for the independent route inventory check.
func (router *Router) Registrations() []string {
	keys := make([]string, 0, len(router.registered))
	for key := range router.registered {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// Validate checks registered handlers and their proof wiring. The complete controller
// and node-host inventories must be combined and checked with capability.ValidateRoutes.
func (router *Router) Validate() error {
	for _, endpoint := range router.registered {
		route, found := capability.LookupRoute(endpoint.Method, endpoint.Pattern)
		if !found {
			return fmt.Errorf("endpoint %s %s: %w", endpoint.Method, endpoint.Pattern, MissingEndpointPolicy())
		}
		if err := router.checkEndpoint(route, endpoint); err != nil {
			return fmt.Errorf("endpoint %s %s: %w", endpoint.Method, endpoint.Pattern, err)
		}
	}
	return nil
}

func (router *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := router.Mux.Handler(r); pattern == "" {
		WriteRouteMiss(w, router.AllowedMethods(r))
		return
	}
	router.Mux.ServeHTTP(w, r)
}

func (router *Router) AllowedMethods(r *http.Request) []string {
	var methods []string
	for _, endpoint := range router.registered {
		methods = append(methods, endpoint.Method)
	}
	return AllowedRouteMethods(router.Mux, r, methods)
}

func AllowedRouteMethods(mux *http.ServeMux, r *http.Request, methods []string) []string {
	var allowed []string
	seen := make(map[string]bool)
	probe := r.Clone(r.Context())
	for _, method := range methods {
		if seen[method] {
			continue
		}
		seen[method] = true
		probe.Method = method
		if _, pattern := mux.Handler(probe); pattern != "" && strings.HasPrefix(pattern, method+" ") {
			allowed = append(allowed, method)
		}
	}
	slices.Sort(allowed)
	return allowed
}

func WriteRouteMiss(w http.ResponseWriter, allowed []string) {
	if len(allowed) != 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		WritePolicyError(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "method is not registered"})
		return
	}
	WritePolicyError(w, &apierr.Error{Code: apierr.NotFound, Message: "route not found"})
}

func (router *Router) checkEndpoint(route capability.RoutePolicy, endpoint controllerwire.Endpoint) error {
	if strings.Contains(endpoint.Pattern, "{run}") && endpoint.ResolveTarget == nil {
		return MissingEndpointPolicy()
	}
	if route.Authentication != endpoint.Authentication {
		return MissingEndpointPolicy()
	}
	if route.Authentication == capability.AuthCapability {
		if endpoint.Decode == nil || endpoint.Verify != nil || router.authorization.Authenticator == nil || router.authorization.Bindings == nil {
			return MissingEndpointPolicy()
		}
	} else if route.Authentication != capability.AuthPublic && endpoint.Verify == nil {
		return MissingEndpointPolicy()
	}
	return nil
}

func (router *Router) serveEndpoint(w http.ResponseWriter, r *http.Request, endpoint controllerwire.Endpoint) error {
	// safety: this is the registered pattern, so caller text cannot enter audit route labels.
	if router.authorization.NoteRoute != nil {
		router.authorization.NoteRoute(r.Context(), endpoint.Pattern)
	}
	route, found := capability.LookupRoute(endpoint.Method, endpoint.Pattern)
	if !found {
		return MissingEndpointPolicy()
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
	authorized, err := router.authorize(r.Context(), endpoint, route, presentation, decodeErr, func(principal capability.Principal) error {
		if !requiresCSRF(r.Method, principal.Class) {
			return nil
		}
		if router.authorization.CheckCSRF == nil {
			return MissingEndpointPolicy()
		}
		return router.authorization.CheckCSRF(r, principal)
	})
	if err != nil {
		return err
	}
	if evidence := presentation.Evidence; evidence.ResultDigest != "" {
		b := authorized.Binding
		if evidence.ResultAttempt != nil {
			b.Attempt = *evidence.ResultAttempt
		}
		authorized.Result = &capability.RecordedResult{Actor: authorized.Principal.Authority, Method: r.Method, Pattern: route.Path, RunID: authorized.Target.RunID, NodeID: authorized.Target.NodeID, ClaimID: authorized.Target.ClaimID, Generation: b.Generation, Attempt: b.Attempt, Digest: evidence.ResultDigest}
	}
	return endpoint.Handle(w, r, authorized)
}

// Authorize applies the registered route resolver and policy to embedded requests.
func (router *Router) Authorize(ctx context.Context, method, pattern string, presentation capability.Presentation) (context.Context, capability.Authorized, error) {
	if presentation.Class != capability.CredentialBearer {
		return ctx, capability.Authorized{}, MissingEndpointPolicy()
	}
	endpoint, ok := router.registered[method+" "+pattern]
	route, found := capability.LookupRoute(method, pattern)
	if !ok || !found || route.Authentication != capability.AuthCapability {
		return ctx, capability.Authorized{}, MissingEndpointPolicy()
	}
	ctx = router.context(ctx)
	authorized, err := router.authorize(ctx, endpoint, route, presentation, nil, nil)
	return ctx, authorized, err
}

func (router *Router) authorize(ctx context.Context, endpoint controllerwire.Endpoint, route capability.RoutePolicy, presentation capability.Presentation, decodeErr error, beforeAuthorize func(capability.Principal) error) (capability.Authorized, error) {
	if err := router.checkEndpoint(route, endpoint); err != nil {
		return capability.Authorized{}, err
	}
	authenticated, err := router.authorization.Authenticator.Authenticate(ctx, presentation, route)
	if err != nil {
		return capability.Authorized{}, err
	}
	if router.authorization.NotePrincipal != nil {
		router.authorization.NotePrincipal(ctx, authenticated.Principal)
	}
	if route.Owner == capability.OwnerTeam && presentation.Evidence.Target.Team == "" {
		presentation.Evidence.Target.Team = authenticated.Principal.Capability.Team
	}
	if endpoint.ResolveTarget != nil {
		presentation.Evidence.Target, err = endpoint.ResolveTarget(ctx, authenticated.Principal, presentation.Evidence.Target)
		if err != nil {
			return capability.Authorized{}, err
		}
	}
	if decodeErr != nil {
		return capability.Authorized{}, decodeErr
	}
	if beforeAuthorize != nil {
		if err := beforeAuthorize(authenticated.Principal); err != nil {
			return capability.Authorized{}, err
		}
	}
	authorized, err := capability.Authorize(ctx, router.authorization.Bindings, route, authenticated, presentation.Evidence.Target)
	if err != nil && presentation.Evidence.Target.Data != nil && router.authorization.DataError != nil {
		err = router.authorization.DataError(err)
	}
	return authorized, err
}

func (router *Router) presentation(r *http.Request) (capability.Presentation, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 0 {
		if len(values) == 1 {
			fields := strings.Fields(values[0])
			if len(fields) == 2 {
				switch {
				case strings.EqualFold(fields[0], "Bearer"):
					return capability.Presentation{Raw: fields[1], Class: capability.CredentialBearer}, nil
				case strings.EqualFold(fields[0], "Session"):
					return capability.Presentation{Raw: fields[1], Class: capability.CredentialSession}, nil
				}
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

func MissingEndpointPolicy() *apierr.Error {
	return &apierr.Error{Code: apierr.PolicyMissing, Message: "route authorization policy is incomplete"}
}

func WritePolicyError(w http.ResponseWriter, err error) {
	var failure *apierr.Error
	if !errors.As(err, &failure) {
		failure = &apierr.Error{Code: apierr.Internal, Message: "request failed"}
	}
	var encoded bytes.Buffer
	if encodeErr := apierr.WriteJSON(&encoded, failure); encodeErr != nil {
		err = errors.Join(err, encodeErr)
		failure = &apierr.Error{Code: apierr.Internal, Message: "request failed"}
	}
	if apierr.Status(failure.Code) >= http.StatusInternalServerError {
		apierr.NoteResponseError(w, err)
		safe := &apierr.Error{Code: failure.Code, Message: "request failed"}
		switch delay := failure.Details["retry_after_ms"].(type) {
		case int, int64, float64:
			safe.Details = map[string]any{"retry_after_ms": delay}
		}
		failure = safe
	}
	if err := apierr.WriteHTTP(w, failure); err != nil {
		// safety: internal errors may contain credentials; log only the safe code.
		slog.Error("write API error response", "code", failure.Code)
	}
}

// safety: responses carry credentials or tenant data that browser disk caches
// and intermediaries must not keep.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (router *Router) context(ctx context.Context) context.Context {
	if router.authorization.Context != nil {
		return router.authorization.Context(ctx)
	}
	return ctx
}
