package store

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestClaimUnitLocksOwnerBeforeRun(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, recorder := newRecordingStore(t)
			rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)`)
			rewriteSchemaExec(t, s.db, `INSERT INTO team_counters(team) VALUES ('a'),('b')`)
			s, demand := unitClaimFixtureStore(t, s)
			fundUnitClaim(t, s)
			demand.Target = &UnitRef{Kind: kind, RunID: "run"}
			ownerQuery := "SELECT id FROM triggers WHERE"
			if kind == capability.UnitEval {
				root := authorityRoot("launcher", "")
				root.Subject.Kind = capability.SubjectService
				root.Grants = []capability.Grant{capability.ClaimsLaunch}
				authorityPersist(t, s, root)
				demand.CapabilityPrefix = root.Prefix
				demand.Target.NodeID, demand.Target.SourceGeneration = "job", 7
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
				rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, s.now().Add(time.Hour).UnixNano())
				rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','job',7,'pending',3)`)
				ownerQuery = "SELECT run_id FROM eval_units WHERE"
			}
			recorder.reset()
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil {
				t.Fatal(err)
			}
			ownerAt, runAt := -1, -1
			for i, statement := range recorder.snapshot() {
				if ownerAt < 0 && strings.HasPrefix(statement, ownerQuery) {
					ownerAt = i
				}
				if runAt < 0 && strings.HasPrefix(statement, "SELECT id FROM runs WHERE") {
					runAt = i
				}
			}
			if ownerAt < 0 || runAt < 0 || ownerAt >= runAt {
				t.Fatalf("owner lock index=%d, run lock index=%d", ownerAt, runAt)
			}
		})
	}
}

func TestClaimQueueLedgerLockOrder(t *testing.T) {
	for _, inverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "queue then ledger", true: "ledger then queue"}[inverse], func(t *testing.T) {
			s, recorder := newRecordingStore(t)
			recorder.reset()
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				if !inverse {
					if err := lockClaimQueueTx(t.Context(), tx); err != nil {
						return err
					}
				}
				if _, err := lockLedgerTx(t.Context(), tx, "a"); err != nil {
					return err
				}
				// Ignoring a refused acquisition must still roll back the transaction.
				_ = lockClaimQueueTx(t.Context(), tx)
				return nil
			})
			if inverse {
				if err == nil || !strings.Contains(err.Error(), "claim lock order") {
					t.Fatalf("inverse lock order committed: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			locks := 0
			for _, query := range recorder.snapshot() {
				if strings.HasPrefix(query, "SELECT ordinal FROM claim_awards") {
					locks++
				}
			}
			want := 1
			if inverse {
				want = 0
			}
			if locks != want {
				t.Fatalf("queue acquisitions = %d, want %d", locks, want)
			}
		})
	}
}
