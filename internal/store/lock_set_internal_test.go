package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLockSetAcquiresEachRowOnceInGlobalOrder(t *testing.T) {
	s, recorder := newRecordingStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('z',1,1)`)
	recorder.reset()
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		discover := func() lockSet {
			var set lockSet
			set.add(lockTeam, false, "z")
			set.add(lockTeam, true, "a")
			set.add(lockTeam, true, "z")
			return set
		}
		if err := acquireLockSet(t.Context(), tx, discover); err != nil {
			return err
		}
		return acquireLockSet(t.Context(), tx, discover)
	})
	if err != nil {
		t.Fatal(err)
	}
	var acquired []string
	for _, call := range recorder.snapshotCalls() {
		if call.statement == "SELECT name FROM teams WHERE name=?" {
			acquired = append(acquired, fmt.Sprint(call.args[0].Value))
		}
	}
	if !slices.Equal(acquired, []string{"a", "z"}) {
		t.Fatalf("lock acquisitions = %v", acquired)
	}
}

func TestAdministratorProbeHandlesDifferentAncestryDepths(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("a-root", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.OperatorLimits}
	authorityPersist(t, s, root)
	child := authorityRoot("z-child", "")
	child.Subject, child.Grants = root.Subject, root.Grants
	child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	authorityPersist(t, s, child)
	if exists, err := s.AsOperator().HasDeploymentAdministrator(t.Context()); err != nil || exists {
		t.Fatalf("snapshot administrator probe: exists=%v err=%v", exists, err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		exists, err := tx.operator.hasDeploymentAdministratorTx(t.Context(), tx)
		if exists && err == nil {
			return errors.New("unprivileged roots counted as an administrator")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLockSetChangedDiscoveryRetriesWithoutCommittingEarlierAttempt(t *testing.T) {
	s := authorityStore(t)
	attempts, discoveries := 0, 0
	err := retryConflict(t.Context(), func() error {
		attempts++
		return s.inTxOnce(t.Context(), func(tx *storeTx) error {
			if _, err := tx.ExecContext(t.Context(), `UPDATE teams SET display_name=display_name || 'x' WHERE name='a'`); err != nil {
				return err
			}
			return acquireLockSet(t.Context(), tx, func() lockSet {
				discoveries++
				team := "a"
				if discoveries > 1 {
					team = "b"
				}
				var set lockSet
				set.add(lockTeam, true, team)
				return set
			})
		})
	}, func(context.Context, time.Duration) error { return nil })
	if err != nil || attempts != 2 {
		t.Fatalf("discovery retry: attempts=%d err=%v", attempts, err)
	}
	var name string
	if err := s.db.QueryRow(`SELECT display_name FROM teams WHERE name='a'`).Scan(&name); err != nil || name != "x" {
		t.Fatalf("unrevalidated mutation committed: name=%q err=%v", name, err)
	}
}

func TestLockSetRejectsLateEarlierRowsAndUpgrades(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprint(upgrade), func(t *testing.T) {
			s := authorityStore(t)
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				if err := acquireLockSet(t.Context(), tx, func() lockSet {
					var set lockSet
					set.add(lockTeam, false, "b")
					return set
				}); err != nil {
					return err
				}
				return acquireLockSet(t.Context(), tx, func() lockSet {
					var set lockSet
					team := "a"
					if upgrade {
						team = "b"
					}
					set.add(lockTeam, true, team)
					return set
				})
			})
			if err == nil || errors.Is(err, errAuthorityChanged) {
				t.Fatalf("incomplete declaration accepted or retried: %v", err)
			}
		})
	}
}
