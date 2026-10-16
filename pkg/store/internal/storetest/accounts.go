package storetest

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// AccountAuthority authenticates a fresh direct session for an existing fixture account.
func AccountAuthority(t testing.TB, st *store.Store, id string) capability.Authorized {
	t.Helper()
	ctx := t.Context()
	memberships, err := st.AccountMemberships(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if len(memberships) > 0 {
		raw, _, _, err = st.CreateAccountSession(ctx, store.Account{ID: id}, memberships[0].Team, time.Hour)
	} else {
		identities, readErr := st.AccountIdentities(ctx, id)
		if readErr != nil || len(identities) == 0 {
			t.Fatalf("account %s needs a sign-in identity: %v", id, readErr)
		}
		raw, _, _, err = st.CreateIdentityAccountSession(ctx, store.Account{ID: id}, "", identities[0].Provider, identities[0].Subject, time.Hour)
	}
	if err != nil {
		t.Fatal(err)
	}
	return SessionAuthority(t, st, raw)
}

// SessionAuthority authenticates an existing direct account fixture session.
func SessionAuthority(t testing.TB, st *store.Store, raw string) capability.Authorized {
	t.Helper()
	ctx := t.Context()
	route, _ := capability.LookupRoute("POST", "/api/v1/me/active-team")
	auth, err := capability.NewAuthenticator(st).Authenticate(ctx, capability.Presentation{Class: capability.CredentialSession, Raw: raw}, route)
	if err != nil {
		t.Fatal(err)
	}
	a, err := capability.Authorize(ctx, st, route, auth, capability.Target{AccountID: auth.Principal.Capability.Subject.ID})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
