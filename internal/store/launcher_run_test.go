package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLauncherRunTerminalRequiresDeploymentAuthority(t *testing.T) {
	s, demand, _, _ := launcherSnapshotFixture(t, capability.UnitPlan)
	op := s.AsOperator()
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	if terminal, err := op.LauncherRunTerminal(t.Context(), ref, "a", "run"); err != nil || terminal {
		t.Fatalf("live run: %v, %v", terminal, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET status='success',finished_at=1 WHERE team='a' AND id='run'`)
	if terminal, err := op.LauncherRunTerminal(t.Context(), ref, "a", "run"); err != nil || !terminal {
		t.Fatalf("terminal run: %v, %v", terminal, err)
	}
	for _, team := range []string{"b", "a' OR 1=1 --"} {
		if terminal, err := op.LauncherRunTerminal(t.Context(), ref, team, "run"); err != nil || terminal {
			t.Fatalf("foreign team %q: %v, %v", team, terminal, err)
		}
	}
	root := authorityRoot("not-launcher", "a")
	authorityPersist(t, s, root)
	if terminal, err := op.LauncherRunTerminal(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, "a", "run"); err == nil || terminal {
		t.Fatalf("non-launcher: %v, %v", terminal, err)
	}
	restricted := authorityRoot("restricted-launcher", "")
	restricted.Subject.Kind = capability.SubjectService
	restricted.Grants = []capability.Grant{capability.ClaimsLaunch}
	restricted.Repositories = []string{"github.com/foreign/*"}
	authorityPersist(t, s, restricted)
	if terminal, err := op.LauncherRunTerminal(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: restricted.Prefix}, "a", "run"); err == nil || terminal {
		t.Fatalf("repository-scoped launcher escaped: %v, %v", terminal, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, ref.ID)
	if terminal, err := op.LauncherRunTerminal(t.Context(), ref, "a", "run"); err == nil || terminal {
		t.Fatalf("revoked launcher: %v, %v", terminal, err)
	}
}
