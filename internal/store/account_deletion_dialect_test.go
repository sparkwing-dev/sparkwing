package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestAccountDeletionHyphenatedSlugsAcrossDialects(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*testing.T) *store.Store
	}{
		{"sqlite", func(t *testing.T) *store.Store { return storetest.OpenSQLite(t) }}, {"postgres", func(t *testing.T) *store.Store { return storetest.OpenPostgres(t) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.open(t)
			owner := signIn(t, s, "slug-owner", "slug-owner@example.test")
			for _, slug := range []store.Team{"my-team", "myapp"} {
				if _, err := s.AsOperator().CreateAccountTeam(t.Context(), storetest.AccountAuthority(t, s, owner.Account.ID), store.TeamInfo{Slug: slug}); err != nil {
					t.Fatal(err)
				}
			}
			a := storetest.AccountAuthority(t, s, owner.Account.ID)
			if _, err := s.AsOperator().DeleteOwnAccount(t.Context(), a, owner.Account.ID, []string{"myapp", string(owner.PersonalTeam), "my-team"}); err != nil {
				t.Fatalf("hyphenated deletion on %s: %v", tc.name, err)
			}
		})
	}
}
