package store

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestAuthorityGraceDeadlineFencesDescendantsProperty(t *testing.T) {
	s := authorityStore(t)
	now := time.Unix(1700000000, 123456789).UTC()
	s.clock = func() time.Time { return now }
	random := rand.New(rand.NewPCG(253, 774))
	for i := range 24 {
		start := now
		end := now.Add(time.Duration(random.Int64N(int64(time.Hour))) + 1)
		root := authorityRoot(fmt.Sprintf("root-%d", i), "a")
		root.RevokedAt = &end
		authorityPersist(t, s, root)
		child := authorityRoot(fmt.Sprintf("child-%d", i), "a")
		child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
		authorityPersist(t, s, child)
		for _, at := range []time.Time{start, end.Add(-time.Nanosecond), end, end.Add(time.Nanosecond)} {
			now = at
			for _, c := range []capability.Capability{root, child} {
				_, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix})
				if (err == nil) != at.Before(end) {
					t.Fatalf("resolve %s at %s: %v", c.Prefix, at, err)
				}
				_, err = s.ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: sha256.Sum256([]byte(c.Prefix)), Audience: capability.Controller}, capability.RequestEvidence{})
				if (err == nil) != at.Before(end) {
					t.Fatalf("snapshot %s at %s: %v", c.Prefix, at, err)
				}
			}
		}
	}
}

func TestDeploymentReadinessRespectsRotationDeadlineProperty(t *testing.T) {
	property := func(seconds uint16) bool {
		s := authorityStore(t)
		now := time.Unix(1800000000, 0).UTC()
		s.clock = func() time.Time { return now }
		deadline := now.Add((time.Duration(seconds) + 1) * time.Second)
		c := authorityRoot("operator", "")
		c.Subject.Kind = capability.SubjectOperator
		c.Grants = []capability.Grant{capability.OperatorTokens}
		c.RevokedAt = &deadline
		authorityPersist(t, s, c)
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}
		if _, err := s.ResolveAuthority(t.Context(), ref); err != nil {
			return false
		}
		if ready, err := s.HasDeploymentAdministrator(t.Context()); err != nil || !ready {
			return false
		}
		now = deadline
		if _, err := s.ResolveAuthority(t.Context(), ref); err == nil {
			return false
		}
		ready, err := s.HasDeploymentAdministrator(t.Context())
		return err == nil && !ready
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
