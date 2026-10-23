package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRequestTenantSharesOneHandleAcrossPorts(t *testing.T) {
	calls := 0
	s := New(nil, nil).WithTenantResolver(func(context.Context, store.Team) (TenantHandle, error) {
		calls++
		return queueTeamReader{team: "acme"}, nil
	})
	ctx := context.WithValue(t.Context(), requestTenantKey{}, &requestTenant{})
	first, err := s.queueReader(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.requestTenant(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || calls != 1 {
		t.Fatalf("handle changed or resolved %d times", calls)
	}
	if _, err := s.requestTenant(ctx, "other"); err == nil {
		t.Fatal("request widened to another tenant")
	}
	if calls != 1 {
		t.Fatal("foreign team reached resolver")
	}
	next := context.WithValue(t.Context(), requestTenantKey{}, &requestTenant{})
	if _, err := s.requestTenant(next, "acme"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("another request reused the old tenant handle")
	}
}

func TestRequestTenantRejectsForeignOrMissingHandle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		handle TenantHandle
		err    error
		code   apierr.Code
	}{
		{"foreign", queueTeamReader{team: "other"}, nil, apierr.PolicyMissing},
		{"missing", nil, nil, apierr.PolicyMissing},
		{"unknown", nil, store.ErrUnknownTeam, apierr.NotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(nil, nil).WithTenantResolver(func(context.Context, store.Team) (TenantHandle, error) { return tc.handle, tc.err })
			_, err := s.queueReader(t.Context(), "acme")
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
}
