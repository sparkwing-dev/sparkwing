package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type storeLockTier string

const (
	lockGlobal       storeLockTier = "global advisories"
	lockLease        storeLockTier = "singleton leases"
	lockSettings     storeLockTier = "settings and signup gates"
	lockTeam         storeLockTier = "teams"
	lockAccount      storeLockTier = "accounts"
	lockMembership   storeLockTier = "memberships"
	lockAuthority    storeLockTier = "authority"
	lockInstallation storeLockTier = "GitHub installations"
	lockCredential   storeLockTier = "subscriptions and credentials"
	lockSchedule     storeLockTier = "schedules and intake"
	lockTrigger      storeLockTier = "trigger owners"
	lockEval         storeLockTier = "eval owners"
	lockRun          storeLockTier = "runs"
	lockNode         storeLockTier = "nodes"
	lockConcurrency  storeLockTier = "concurrency entries, holders and waiters"
	lockUpload       storeLockTier = "upload tickets"
	lockDigest       storeLockTier = "blob digest advisories"
	lockQuota        storeLockTier = "quota dimensions"
	lockBilling      storeLockTier = "billing and counters"
	lockRecord       storeLockTier = "records and metadata"
)

var storeLockOrder = [...]storeLockTier{
	lockGlobal, lockLease, lockSettings, lockTeam, lockAccount, lockMembership,
	lockAuthority, lockInstallation, lockCredential, lockSchedule, lockTrigger,
	lockEval, lockRun, lockNode, lockConcurrency, lockUpload, lockDigest,
	lockQuota, lockBilling, lockRecord,
}

func compareLockTiers(a, b storeLockTier) int {
	return cmp.Compare(slices.Index(storeLockOrder[:], a), slices.Index(storeLockOrder[:], b))
}

// lockAuthorityThenRunTx takes a writer's authority chain, then its run.
// Renewal holds authority while it waits for the run, so taking the run first
// deadlocks against it (B15).
func lockAuthorityThenRunTx(ctx context.Context, tx *storeTx, team Team, runID string, actor Actor) error {
	if _, err := resolveActorTx(ctx, tx, actor); err != nil {
		return err
	}
	return lockStateRunTx(ctx, tx, team, runID)
}

type authorityLock struct {
	ref   capability.AuthorityRef
	depth int
	write bool
}

func compareAuthorityLocks(a, b authorityLock) int {
	return compareStoreLocks(authorityLockKey(a), authorityLockKey(b))
}

func authorityLockKey(a authorityLock) storeLockKey {
	return storeLockKey{tier: lockAuthority, depth: a.depth, keys: [3]string{string(a.ref.Kind), a.ref.ID}}
}

type storeLockKey struct {
	tier       storeLockTier
	depth      int
	keys       [3]string
	generation int64
}

func compareStoreLocks(a, b storeLockKey) int {
	if order := cmp.Or(compareLockTiers(a.tier, b.tier), cmp.Compare(b.depth, a.depth)); order != 0 {
		return order
	}
	for i := range a.keys {
		if order := strings.Compare(a.keys[i], b.keys[i]); order != 0 {
			return order
		}
	}
	return cmp.Compare(a.generation, b.generation)
}

// Row-lock clauses are spelled only here (TestRowLockClausesLiveInLockOrder).
// That fixes where a lock's strength is written, not the order callers take
// locks in; each transaction still orders its own acquisitions. SQLite
// serializes writers and takes no row locks; snapshot reads take none either.

func (s *Store) forUpdate() string {
	if s.dialect == DialectPostgres {
		return " FOR UPDATE"
	}
	return ""
}

func (t *storeTx) forUpdate() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR UPDATE"
	}
	return ""
}

func (t *storeTx) forKeyShare() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR KEY SHARE"
	}
	return ""
}

func (t *storeTx) forShare() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR SHARE"
	}
	return ""
}

// safety: FOR UPDATE also blocks the key-share lock an event insert's foreign
// key takes on its run while holding the run's sequence lock, so a transaction
// that locks its run and then appends an event deadlocks with that insert.
func (t *storeTx) forNoKeyUpdate() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR NO KEY UPDATE"
	}
	return ""
}

// tryLockEntryTx takes a concurrency entry for update unless another
// transaction holds it, and reports that it is busy instead of waiting.
// SQLite serializes writers, so there it never reports busy.
func tryLockEntryTx(ctx context.Context, tx *storeTx, team Team, key string) (busy bool, err error) {
	if tx.dialect != DialectPostgres || tx.snapshotRead {
		return false, nil
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM concurrency_entries WHERE team = ? AND key = ? FOR UPDATE SKIP LOCKED`, string(team), key).Scan(&one)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM concurrency_entries WHERE team = ? AND key = ?`, string(team), key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// controllerEpochLockSQL is the first lock every PostgreSQL write transaction takes: a share lock on the
// promoted controller's epoch, so a promotion's epoch bump waits for in-flight writes and a superseded
// controller's next transaction sees the new epoch and stops.
const controllerEpochLockSQL = `SELECT convert_from(value,'UTF8')::bigint FROM settings WHERE key='controller_epoch' FOR SHARE`
