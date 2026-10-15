package main

import (
	"context"
	"errors"
	"testing"
)

type adminReaderFunc func(context.Context) (bool, error)

func (f adminReaderFunc) HasDeploymentAdministrator(ctx context.Context) (bool, error) { return f(ctx) }

func TestCheckRequireAuthUsesCapabilityRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready bool
		err   error
	}{
		{"present", true, nil},
		{"absent", false, nil},
		{"unreadable", true, errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			reader := adminReaderFunc(func(ctx context.Context) (bool, error) {
				calls++
				if ctx != t.Context() {
					t.Fatal("context lost")
				}
				return tc.ready, tc.err
			})
			if err := checkRequireAuth(t.Context(), reader, false); err != nil || calls != 0 {
				t.Fatal("optional gate read administrator state")
			}
			err := checkRequireAuth(t.Context(), reader, true)
			if (err == nil) != (tc.ready && tc.err == nil) || calls != 1 {
				t.Fatalf("gate: calls=%d err=%v", calls, err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatal("backend failure lost")
			}
		})
	}
	if err := checkRequireAuth(t.Context(), nil, true); err == nil {
		t.Fatal("missing capability reader opened startup")
	}
	if err := checkRequireAuth(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
}
