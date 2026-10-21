package store

import (
	"path/filepath"
	"testing"
)

func TestAuditEventsRejectMutationAndSurviveRunRemoval(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return s.AsOperator().RecordBusinessEvent(tx, BusinessEvent{Team: "a", Kind: "secret.released", SubjectID: "release", RunID: "already-deleted-run", Attrs: map[string]any{"name": "KEY"}})
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE audit_events SET actor='forged'`,
		`DELETE FROM audit_events`,
	} {
		if _, err := s.db.Exec(q); err == nil {
			t.Fatalf("audit mutation accepted: %s", q)
		}
	}
	rewriteSchemaExec(t, s.db, `INSERT OR REPLACE INTO audit_events(id,ts,team,kind,subject_id,run_id,actor) SELECT id,ts,team,kind,subject_id,run_id,'forged' FROM audit_events`)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return tx.operator.RecordBusinessEvent(tx, BusinessEvent{Team: "a", Kind: "probe.once", SubjectID: "fact", Actor: "original"})
	}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT OR REPLACE INTO audit_events(id,ts,team,kind,subject_id,actor) VALUES ('replacement',1,'a','probe.once','fact','forged')`)
	var actor string
	if err := s.db.QueryRow(`SELECT actor FROM audit_events WHERE kind='probe.once' AND id<>'replacement'`).Scan(&actor); err != nil || actor != "original" {
		t.Fatalf("replacement altered audit: %q %v", actor, err)
	}
	rewriteSchemaCount(t, s.db, "audit_events WHERE kind='probe.once'", 2)
	tenant := &Tenant{s: s, team: "a"}
	rows, err := tenant.BusinessEvents(t.Context(), "secret.released")
	if err != nil || len(rows) != 1 || rows[0].RunID != "already-deleted-run" || rows[0].Actor != "" {
		t.Fatalf("retained audit=%+v: %v", rows, err)
	}
}

func TestPrivilegedMutationRollsBackWithoutAudit(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	a := teamAuthorization(t, s, owner, "a", "PATCH", "/api/v1/team")
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_mutation_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if _, err := tenant.RenameAuthorizedTeam(t.Context(), a, "changed"); err == nil {
		t.Fatal("unaudited mutation committed")
	}
	var name string
	if err := s.db.QueryRow(`SELECT display_name FROM teams WHERE name='a'`).Scan(&name); err != nil || name == "changed" {
		t.Fatalf("uncommitted name=%q: %v", name, err)
	}
}

func TestEveryRunDeletionLeavesTombstone(t *testing.T) {
	for _, mode := range []string{"raw", "purge"} {
		t.Run(mode, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "runs.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
			rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'p','success',?,'request',1,'gh:1','host','execute',?,'account',1,2,'r30',30)`, []byte(`{"version":1}`), []byte(`[]`))
			if mode == "raw" {
				rewriteSchemaExec(t, s.db, `DELETE FROM runs WHERE team='a'`)
			} else if err := s.AsOperator().deleteTenantRows(t.Context(), "a"); err != nil {
				t.Fatal(err)
			}
			rows, err := (&Tenant{s: s, team: "a"}).BusinessEvents(t.Context(), "run.deleted")
			if err != nil || len(rows) != 1 || rows[0].RunID != "run" {
				t.Fatalf("tombstone=%+v: %v", rows, err)
			}
		})
	}
}

func TestMemberRemovalRequiresCommittedAudit(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	authorityMember(t, s, "member", RoleReader)
	tenant := &Tenant{s: s, team: "a"}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_membership_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"}); err == nil {
		t.Fatal("unaudited removal committed")
	}
	if role, err := tenant.MemberRole(t.Context(), "member"); err != nil || role != RoleReader {
		t.Fatalf("removal did not roll back: %s %v", role, err)
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_membership_audit`)
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"}); err != nil {
		t.Fatal(err)
	}
	rows, err := tenant.BusinessEvents(t.Context(), "team.mutated")
	if err != nil || len(rows) != 1 || rows[0].Actor != "owner" || rows[0].SubjectID != "member" || rows[0].Attrs["operation"] != "membership.removed" {
		t.Fatalf("membership audit=%+v: %v", rows, err)
	}
}
