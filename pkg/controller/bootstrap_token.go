package controller

import (
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// BootstrapAdminPrincipal labels the token [EnsureBootstrapAdminToken]
// writes, so an operator listing tokens can tell the provisioned
// credential apart from one an admin minted through the API.
const BootstrapAdminPrincipal = "bootstrap:admin"

// EnsureBootstrapAdminToken stores raw as an admin token when the tokens
// table holds no rows, and reports whether it created one. An empty raw
// creates nothing. Provisioning calls it before the listener binds, which
// is what lets a controller come up with --require-auth satisfied and no
// window in which it serves unauthenticated.
//
// raw is the credential the operator already holds; only its hash reaches
// the database, the same as for a token minted through POST /api/v1/tokens.
// It has to carry the shape [store.ValidateRawToken] describes, because a
// bearer lookup selects on the indexed prefix.
//
// A table that already holds a token is left alone, so restarting with the
// same secret mounted neither duplicates the row nor revives a revoked one.
func EnsureBootstrapAdminToken(st *store.Store, raw string, now time.Time) (bool, error) {
	if raw == "" {
		return false, nil
	}
	if st == nil {
		return false, errors.New("bootstrap admin token: no store")
	}
	return st.CreateTokenIfNoneExist(raw, BootstrapAdminPrincipal, store.TokenKindUser,
		[]string{ScopeAdmin}, now)
}
