// Package routepolicy applies the shared controller and process route contract.
package routepolicy

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// Authorization supplies transport checks and controller request observations.
type Authorization struct {
	Authenticator capability.Authenticator
	Bindings      capability.BindingChecker
	Identify      func(*http.Request) (capability.Presentation, error)
	CheckCSRF     func(*http.Request, capability.Principal) error
	Context       func(context.Context) context.Context
	NoteRoute     func(context.Context, string)
	NotePrincipal func(context.Context, capability.Principal)
	DataError     func(error) error
}

// Router matches a registered route before applying its authorization policy.
type Router struct {
	Mux           *http.ServeMux
	authorization Authorization
	registered    map[string]controllerwire.Endpoint
}
