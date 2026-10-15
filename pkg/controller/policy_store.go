package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (s *Server) storeRouteAuthorization() RouteAuthorization {
	if s.store == nil {
		return RouteAuthorization{}
	}
	return RouteAuthorization{
		Authenticator: capability.NewAuthenticator(s.store), Bindings: s.store,
		Identify: func(r *http.Request) (capability.Presentation, error) {
			return capability.Presentation{Raw: sessionCookie(r, s.cookiesSecure()), Class: capability.CredentialSession}, nil
		},
	}
}
