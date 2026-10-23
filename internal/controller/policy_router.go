package controller

import (
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
)

func (s *Server) registerControllerEndpoints(mux *registeredMux, endpoints []controllerwire.Endpoint) {
	policy := routepolicy.New(controllerAuthorization(s.authorization, s.resolveTenant))
	policy.Mux = mux.ServeMux

	for _, endpoint := range endpoints {
		if err := policy.Register(s.budgetEndpoint(s.tenantEndpoint(endpoint))); err != nil {
			panic(err)
		}
	}
	mux.registrations = append(mux.registrations, policy.Registrations()...)
}

type registeredMux struct {
	*http.ServeMux
	registrations []string
}

func newRegisteredMux() *registeredMux {
	return &registeredMux{ServeMux: http.NewServeMux()}
}

func (mux *registeredMux) Handle(pattern string, handler http.Handler) {
	mux.ServeMux.Handle(pattern, handler)
	if strings.Contains(pattern, " ") {
		mux.registrations = append(mux.registrations, pattern)
	}
}

func (mux *registeredMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	mux.Handle(pattern, http.HandlerFunc(handler))
}

// safety: billing routes stay registered on every deployment so the route
// table is the same everywhere, but which handler serves them is chosen
// once, from the declared deployment, when the routes are built.
func (s *Server) meteredOnly(h func(http.ResponseWriter, *http.Request, capability.Authorized) error) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	if s.deployment.Metering() {
		return h
	}
	return func(http.ResponseWriter, *http.Request, capability.Authorized) error {
		return &apierr.Error{Code: apierr.NotFound, Message: "credit billing is unavailable on this deployment"}
	}
}
