package store_test

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestResolveRunIDUsesOnlyTheSelectedTeamsIdentity(t *testing.T) {
	s := storetest.New(t).Open(t)
	a, b := tenantFor(t, s, "selector-a"), tenantFor(t, s, "selector-b")
	seedTenantRun(t, a, "run-a", "pipeline")
	seedTenantRun(t, b, "run-b", "pipeline")
	seedTenantRun(t, a, "01", "pipeline")
	for _, tc := range []struct {
		tenant   *store.Tenant
		selector string
		want     string
	}{
		{a, "run-a", "run-a"},
		{a, "1", "run-a"},
		{b, "1", "run-b"},
		{a, "01", "01"},
		{a, "2", "01"},
		{a, "run-b", ""},
		{b, "run-a", ""},
		{b, "2", ""},
		{a, "", ""},
		{a, "+1", ""},
		{a, "0", ""},
		{a, "-1", ""},
		{a, "9223372036854775808", ""},
	} {
		got, err := tc.tenant.ResolveRunID(t.Context(), tc.selector)
		if tc.want == "" {
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s/%q = %q, %v, want not found", tc.tenant.Team(), tc.selector, got, err)
			}
		} else if err != nil || got != tc.want {
			t.Errorf("%s/%q = %q, %v, want %q", tc.tenant.Team(), tc.selector, got, err, tc.want)
		}
	}
}
