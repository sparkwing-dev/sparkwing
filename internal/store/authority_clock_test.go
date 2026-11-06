package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestAuthorityToleratesBackwardClockStep(t *testing.T) {
	s := authorityStore(t)
	issued := time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC)
	now := issued
	s.clock = func() time.Time { return now }
	expires := issued.Add(time.Minute)
	root := authorityRoot("root", "a")
	root.IssuedAt, root.ExpiresAt = issued, &expires
	authorityPersist(t, s, root)
	child := authorityRoot("child", "a")
	child.IssuedAt = issued
	child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	authorityPersist(t, s, child)
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Digest: child.Digest, Audience: capability.Controller}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
	for _, tt := range []struct {
		name   string
		offset time.Duration
		alive  bool
	}{
		{"issued", 0, true},
		{"backward step", -3500 * time.Millisecond, true},
		{"skew boundary", -30 * time.Second, true},
		{"beyond skew", -30*time.Second - time.Nanosecond, false},
		{"far future issuance", -time.Hour, false},
		{"before expiry", time.Minute - time.Nanosecond, true},
		{"at expiry", time.Minute, false},
		{"after expiry", time.Minute + time.Nanosecond, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now = issued.Add(tt.offset)
			_, readErr := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
			_, resolveErr := s.AsOperator().ResolveAuthority(t.Context(), ref)
			for _, err := range []error{readErr, resolveErr} {
				if tt.alive {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					var refusal *apierr.Error
					if !errors.As(err, &refusal) || refusal.Code != apierr.Unauthenticated {
						t.Fatalf("invalid authority: %v", err)
					}
				}
			}
		})
	}
}
