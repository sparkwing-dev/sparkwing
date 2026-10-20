package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestDeletionLockDiscoveryAcceptsEndedTargetAuthority(t *testing.T) {
	for _, kind := range []string{"team", "account"} {
		t.Run(kind, func(t *testing.T) {
			s, console, actor := consoleFixture(t)
			departed := authorityMember(t, s, "departed", RoleEditor, "b")
			child := authorityRoot("departed-child", "b")
			child.Parent = &departed.Authority
			child.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "departed"}
			child.Grants = []capability.Grant{capability.RunsRead}
			authorityPersist(t, s, child)
			orphan := authorityRoot("orphan", "b")
			orphan.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "missing-parent"}
			authorityPersist(t, s, orphan)
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by='departed' WHERE team='b'`)
			rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE account_id='departed'`)
			var err error
			if kind == "team" {
				_, err = console.ConsoleDeleteTeam(t.Context(), actor)
			} else {
				actor.Target.AccountID = "departed"
				_, err = console.ConsoleDeleteAccount(t.Context(), actor)
			}
			if err != nil {
				t.Fatal(err)
			}
			var active int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM capabilities WHERE team='b' AND revoked_at IS NULL`).Scan(&active); err != nil || active != 0 {
				t.Fatalf("ended target credentials survived deletion: count=%d, %v", active, err)
			}
		})
	}
}
