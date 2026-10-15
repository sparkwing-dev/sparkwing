package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
)

func principalName(r *http.Request) string {
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		return p.Name
	}
	return authwire.AnonymousPrincipal
}

func isAdmin(r *http.Request) bool {
	p, ok := PrincipalFromContext(r.Context())
	return !ok || p.HasScope(ScopeAdmin)
}
