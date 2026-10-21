package store

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
		return tx.operator.recordAudit(tx, auditContext{Team: "a", Actor: auditActor{system: auditController}}, unregisteredAuditPayload{Personal: "Jane Doe; person@example.test"})
	})
	if err == nil {
		t.Fatal("unregistered audit payload committed")
	}
	rewriteSchemaCount(t, s.db, "audit_events", 0)
}

func TestAuditWriterPreservesNonAccountProvenance(t *testing.T) {
	for _, name := range []string{"account", "Jane Doe; phone 208-555-0100", "person@example.test", "payment_provider", "system", "sparkwing-controller admin"} {
		t.Run(name, func(t *testing.T) {
			s := authorityStore(t)
			rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','person@example.test',1,1)`)
			for _, kind := range []capability.SubjectKind{capability.SubjectMachine, capability.SubjectService, capability.SubjectOperator, capability.SubjectLocal} {
				p := capability.Principal{Capability: capability.Capability{Subject: capability.Subject{Kind: kind, ID: name}, Binding: capability.Binding{Holder: name}}, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "verified-prefix"}}
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					return tx.operator.recordAudit(tx, auditContext{Team: "a", Actor: auditPrincipalActor(p)}, auditSecretRelease{Name: digestAudit("KEY"), Holder: digestAudit(name)})
				}); err != nil {
					t.Fatal(err)
				}
			}
			events, err := (&Tenant{s: s, team: "a"}).BusinessEvents(t.Context(), "secret.released", 10, "")
			if err != nil || len(events) != 4 {
				t.Fatalf("events=%+v err=%v", events, err)
			}
			seen := map[string]bool{}
			for _, ev := range events {
				if !strings.Contains(ev.Actor, ":") || strings.Contains(ev.Actor, name) || seen[ev.Actor] || ev.Attrs["holder_digest"] != digestAudit(name).String() {
					t.Fatalf("lost provenance or retained name: %+v", ev)
				}
				seen[ev.Actor] = true
			}
			var accounts int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE actor_is_account=1`).Scan(&accounts); err != nil || accounts != 0 {
				t.Fatalf("nonaccount actors marked account: %d %v", accounts, err)
			}
		})
	}
}

func TestAuditWriterRequiresClosedActor(t *testing.T) {
	s := authorityStore(t)
	for _, actor := range []auditActor{{}, {system: "caller-selected"}, {subject: capability.Subject{Kind: "caller-selected", ID: "account"}}, {system: auditController, subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"}}} {
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			return tx.operator.recordAudit(tx, auditContext{Team: "a", Actor: actor}, auditSecretRelease{Name: digestAudit("KEY")})
		}); err == nil {
			t.Fatalf("invalid actor committed: %+v", actor)
		}
	}
	rewriteSchemaCount(t, s.db, "audit_events", 0)
}

func TestAuditWriterPreservesAccountKindWithoutLookup(t *testing.T) {
	s := authorityStore(t)
	p := capability.Principal{Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "verified-account"}}}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return tx.operator.recordAudit(tx, auditContext{Team: "a", Actor: auditPrincipalActor(p)}, auditSecretRelease{Name: digestAudit("KEY")})
	}); err != nil {
		t.Fatal(err)
	}
	var actor string
	var account int
	if err := s.db.QueryRow(`SELECT actor,actor_is_account FROM audit_events WHERE kind='secret.released'`).Scan(&actor, &account); err != nil || actor != "verified-account" || account != 1 {
		t.Fatalf("account provenance depends on account table: actor=%q account=%d err=%v", actor, account, err)
	}
}

func TestAuditWriterDistinguishesPrincipalProvenance(t *testing.T) {
	s := authorityStore(t)
	p := capability.Principal{Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectService, ID: "runner"}, Binding: capability.Binding{Holder: "holder-one"}}, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "authority-one"}}
	for i := range 4 {
		next := p
		if i == 1 {
			next.Authority.ID = "authority-two"
		}
		if i == 2 {
			next.Capability.Binding.Holder = "holder-two"
		}
		if i == 3 {
			next.Capability.Subject.ID = "other-runner"
		}
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			return tx.operator.recordAudit(tx, auditContext{Team: "a", Actor: auditPrincipalActor(next)}, auditSecretRelease{Name: digestAudit("KEY")})
		}); err != nil {
			t.Fatal(err)
		}
	}
	var identities int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT actor) FROM audit_events WHERE kind='secret.released'`).Scan(&identities); err != nil || identities != 4 {
		t.Fatalf("principal provenance lost: identities=%d err=%v", identities, err)
	}
}
