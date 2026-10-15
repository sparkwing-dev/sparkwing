package store

import (
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

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
