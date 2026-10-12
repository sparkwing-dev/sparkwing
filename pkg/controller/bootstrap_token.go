package controller

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// BootstrapAdminPrincipal labels the token [EnsureBootstrapAdminToken]
// writes, so an operator listing tokens can tell the provisioned
// credential apart from one an admin minted through the API.
const BootstrapAdminPrincipal = "bootstrap:admin"

// CheckoutServicePrincipal labels the token [EnsureCheckoutToken] writes.
const CheckoutServicePrincipal = "checkout-service"

// EnsureCheckoutToken seeds raw as the checkout service's token, a service
// token carrying only [ScopeCreditsGrant], through [store.Store.SeedToken],
// and reports whether it created one. An empty raw creates nothing; a raw
// whose token was revoked returns [store.ErrSeededTokenRevoked] and is never
// seeded again. The controller's API never mints that scope, so this
// credential, provisioned beside the controller's other secrets, is the only
// way the checkout service gets one.
func EnsureCheckoutToken(ctx context.Context, st *store.Store, raw string, now time.Time) (bool, error) {
	if raw == "" {
		return false, nil
	}
	if st == nil {
		return false, errors.New("checkout token: no store")
	}
	return st.SeedToken(ctx, raw, CheckoutServicePrincipal, store.TokenKindService,
		[]string{ScopeCreditsGrant}, now)
}

// safety: each scope here reaches an operator power, so no API call mints or
// rotates it into a token; only a credential the deployment provisions carries one.
var provisionedOnlyScopes = []string{ScopeCreditsGrant}

func provisionedOnlyScope(scopes []string) string {
	for _, s := range scopes {
		if slices.Contains(provisionedOnlyScopes, s) {
			return s
		}
	}
	return ""
}

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
