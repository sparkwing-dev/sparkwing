package store

import (
	"regexp"
	"testing"
)

// Audit insertion belongs to the typed writer and the database's run tombstone.
func TestAuditInsertHasOneOwner(t *testing.T) {
	insert := regexp.MustCompile(`(?is)\bINSERT\s+(?:OR\s+\w+\s+)?INTO\s+audit_events\b`)
	seen := map[string]bool{}
	for _, stmt := range tenantSQLStatements(t) {
		if !insert.MatchString(stmt.text) {
			continue
		}
		switch stmt.key {
		case "(*Operator).recordAudit", "(*Operator).installRunTombstones":
			seen[stmt.key] = true
		default:
			t.Errorf("%s: audit insert in %s bypasses the typed audit owner", stmt.pos, stmt.key)
		}
	}
	for _, owner := range []string{"(*Operator).recordAudit", "(*Operator).installRunTombstones"} {
		if !seen[owner] {
			t.Errorf("guard did not inspect %s", owner)
		}
	}
}

type unregisteredAuditPayload struct{ Personal string }

func (unregisteredAuditPayload) isAuditPayload() {}

func TestAuditWriterRejectsUnregisteredPayload(t *testing.T) {
	s := authorityStore(t)
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		return tx.operator.recordAudit(tx, auditContext{Team: "a"}, unregisteredAuditPayload{Personal: "Jane Doe; person@example.test"})
	})
	if err == nil {
		t.Fatal("unregistered audit payload committed")
	}
	rewriteSchemaCount(t, s.db, "audit_events", 0)
}

func TestAuditWriterNeverRetainsCallerSelectedActorName(t *testing.T) {
	for _, actor := range []string{"Jane-Doe", "Jane Doe; phone 208-555-0100", "person@example.test", "payment_provider", "system", AuditActorBreakGlass} {
		t.Run(actor, func(t *testing.T) {
			s := authorityStore(t)
			rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','person@example.test',1,1)`)
			rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('person@example.test','',1,1)`)
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				return tx.operator.recordAudit(tx, auditContext{Team: "a", Actor: actor}, auditSecretRelease{Name: digestAudit("KEY"), Holder: digestAudit(actor)})
			}); err != nil {
				t.Fatal(err)
			}
			events, err := (&Tenant{s: s, team: "a"}).BusinessEvents(t.Context(), "secret.released", 10, "")
			if err != nil || len(events) != 1 || events[0].Actor != "" || events[0].Attrs["holder_digest"] != digestAudit(actor).String() {
				t.Fatalf("non-account audit actor: %+v %v", events, err)
			}
		})
	}
}
