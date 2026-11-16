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
	// NoteRefusal observes an authenticated request refused because of the
	// resource it names. It cannot change the response.
	NoteRefusal func(context.Context, Refusal)
	DataError   func(error) error
}

// RefusalClass names why a request was refused for the resource it names.
type RefusalClass string

const (
	// RefusalNotFound: the named run, job or other resource did not resolve in the principal's team.
	RefusalNotFound RefusalClass = "not_found"
	// RefusalTeam: the request named a team other than the principal's.
	RefusalTeam RefusalClass = "team"
	// RefusalBinding: the named resource lies outside the credential's binding, such as another claim or child.
	RefusalBinding RefusalClass = "binding"
)

// Refusal is one authenticated request refused for the resource it names.
// Pattern is the registered route, never the request path.
type Refusal struct {
	Principal capability.Principal
	Method    string
	Pattern   string
	Class     RefusalClass
}

// Router matches a registered route before applying its authorization policy.
type Router struct {
	Mux           *http.ServeMux
	authorization Authorization
	registered    map[string]controllerwire.Endpoint
}
