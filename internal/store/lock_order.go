package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

type storeLockTier string

const (
	lockLedger       storeLockTier = "credit ledgers"
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
	lockSettings, lockTeam, lockAccount, lockMembership,
	lockAuthority, lockInstallation, lockCredential, lockSchedule, lockTrigger,
	lockEval, lockRun, lockNode, lockConcurrency, lockUpload, lockDigest,
	lockQuota, lockLedger, lockBilling, lockRecord,
}

func compareLockTiers(a, b storeLockTier) int {
	return cmp.Compare(slices.Index(storeLockOrder[:], a), slices.Index(storeLockOrder[:], b))
}

// safety: claims take the controller epoch fence, queue arbitration, authority and unit rows, then the ledger;
// arbitration serializes awards without unit ranking locks.
func lockClaimQueueTx(ctx context.Context, tx *storeTx) error {
	if tx.claimQueueLocked {
		return nil
	}
	if tx.ledgerLocked {
		tx.err = errors.New("claim lock order: queue arbitration must precede the ledger")
		return tx.err
	}
	var ordinal int64
	if err := tx.QueryRowContext(ctx, `SELECT ordinal FROM claim_awards WHERE singleton=1`+tx.forUpdate()).Scan(&ordinal); err != nil {
		return err
	}
	tx.claimQueueLocked = true
	return nil
}

// lockAuthorityThenRunTx takes a writer's authority chain, then its run.
// Renewal holds authority while it waits for the run, so taking the run first
// deadlocks against it.
func lockAuthorityThenRunTx(ctx context.Context, tx *storeTx, team Team, runID string, actor runstate.Actor) error {
	if _, err := resolveTeamActorTx(ctx, tx, team, actor, claimantAccepts(actor)); err != nil {
		return err
	}
	return lockStateRunTx(ctx, tx, team, runID)
}

type authorityLock struct {
	ref   capability.AuthorityRef
	depth int
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

type rowLock struct {
	key      storeLockKey
	write    bool
	optional bool
}

func (r rowLock) sameRow(other rowLock) bool {
	return r.key.tier == other.key.tier && r.key.keys == other.key.keys && r.key.generation == other.key.generation
}

// A mutation declares its complete set before resolving live authority. Team
// foreign keys (including audit events) belong here even without an explicit read.
type lockSet struct {
	rows []rowLock
	err  error
}

func (s *lockSet) add(tier storeLockTier, write bool, keys ...string) {
	key := storeLockKey{tier: tier}
	copy(key.keys[:], keys)
	s.rows = append(s.rows, rowLock{key: key, write: write})
}

func (s lockSet) normalized() lockSet {
	for _, row := range slices.Clone(s.rows) {
		if row.key.tier == lockLedger {
			s.add(lockTeam, true, row.key.keys[0])
		}
	}
	slices.SortFunc(s.rows, func(a, b rowLock) int { return compareStoreLocks(a.key, b.key) })
	var rows []rowLock
	for _, row := range s.rows {
		if len(rows) != 0 && rows[len(rows)-1].key == row.key {
			last := &rows[len(rows)-1]
			last.write = last.write || row.write
			last.optional = last.optional && row.optional
		} else {
			rows = append(rows, row)
		}
	}
	s.rows = rows
	return s
}

func discoverAuthorityLocks(ctx context.Context, tx *storeTx, leaves []capability.AuthorityRef, writes ...string) lockSet {
	var set lockSet
	for _, leaf := range leaves {
		chain, err := discoverAuthorityChainTx(ctx, tx, leaf)
		if err != nil {
			set.err = err
			return set
		}
		for _, key := range authorityMembershipKeys(chain) {
			set.rows = append(set.rows, rowLock{key: key})
		}
		for i, node := range chain {
			c, ref := node.principal.Capability, node.principal.Authority
			if c.Team != "" && !slices.Contains(tx.insertedAuthorities, ref) {
				set.add(lockTeam, true, c.Team)
			}
			write := slices.Contains(writes, ref.ID) || ref.Kind == capability.AuthorityCapability && registeredClaimant(c)
			set.rows = append(set.rows, rowLock{key: authorityLockKey(authorityLock{ref: ref, depth: len(chain) - 1 - i}), write: write})
		}
	}
	return set
}

// acquireLockSet owns discovery, ordered acquisition and revalidation. Changed
// discovery restarts inTx, releasing every lock before discovering again.
func acquireLockSet(ctx context.Context, tx *storeTx, discover func() lockSet) error {
	if tx.snapshotRead {
		return nil
	}
	set := discover().normalized()
	if set.err != nil {
		return set.err
	}
	for _, row := range set.rows {
		if row.key.tier == lockTeam && tx.createdTeam != nil && row.key.keys[0] == string(tx.createdTeam.Slug) {
			continue
		}
		if row.key.tier == lockAuthority && slices.Contains(tx.insertedAuthorities, capability.AuthorityRef{Kind: capability.AuthorityKind(row.key.keys[0]), ID: row.key.keys[1]}) {
			continue
		}
		// Nested authority checks reuse the mutation's locks without upgrading them.
		if slices.ContainsFunc(tx.authorityLocks, func(held rowLock) bool {
			return held.sameRow(row) && (held.write || !row.write)
		}) {
			continue
		}
		for _, held := range tx.authorityLocks {
			if compareStoreLocks(row.key, held.key) < 0 || held.sameRow(row) {
				return fmt.Errorf("incomplete authority lock set: %s %v after %s %v", row.key.tier, row.key.keys, held.key.tier, held.key.keys)
			}
		}
		if err := tx.operator.acquireRowLock(ctx, tx, row); err != nil {
			return err
		}
		tx.authorityLocks = append(tx.authorityLocks, row)
	}
	current := discover().normalized()
	if current.err != nil {
		return current.err
	}
	if !slices.Equal(set.rows, current.rows) {
		return errAuthorityChanged
	}
	return nil
}

// Financial writers lock the complete team set before any ledger. Existing weaker
// locks cannot be upgraded, and no new team may be discovered after a ledger is held.
func lockFinancialTeamsTx(ctx context.Context, tx *storeTx, teams ...Team) error {
	if tx.snapshotRead {
		return nil
	}
	teams = slices.Compact(slices.Sorted(slices.Values(teams)))
	for _, team := range teams {
		row := rowLock{key: storeLockKey{tier: lockTeam, keys: [3]string{string(team)}}, write: true}
		if slices.ContainsFunc(tx.authorityLocks, func(held rowLock) bool { return held.sameRow(row) && held.write }) {
			continue
		}
		for _, held := range tx.authorityLocks {
			if held.key.tier == lockTeam && (held.sameRow(row) || held.key.keys[0] > string(team)) {
				tx.err = errors.New("financial lock order: declare all teams for update in sorted order before the ledger")
				return tx.err
			}
		}
		if err := tx.operator.acquireRowLock(ctx, tx, row); err != nil {
			return err
		}
		tx.authorityLocks = append(tx.authorityLocks, row)
	}
	return nil
}

func lockOwnerUnitTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) error {
	key := storeLockKey{keys: [3]string{string(team), string(unit.RunID), string(unit.NodeID)}, generation: unit.SourceGeneration}
	switch unit.Kind {
	case capability.UnitTrigger, capability.UnitPlan:
		key.tier = lockTrigger
	case capability.UnitEval:
		key.tier = lockEval
	default:
		return nil
	}
	return lockWorkRowTx(ctx, tx, key, false)
}

func lockWorkRowTx(ctx context.Context, tx *storeTx, key storeLockKey, nonblocking bool) error {
	if tx.snapshotRead {
		return nil
	}
	if slices.Contains(tx.workLocks, key) {
		return nil
	}
	if tx.ledgerLocked {
		tx.err = errors.New("financial lock order: work must precede the ledger")
		return tx.err
	}
	if err := lockFinancialTeamsTx(ctx, tx, Team(key.keys[0])); err != nil {
		return err
	}
	args := []any{key.keys[0], key.keys[1]}
	mode := tx.forUpdate()
	var query string
	switch key.tier {
	case lockTrigger:
		query = `SELECT id FROM triggers WHERE team=? AND id=?`
	case lockEval:
		query = `SELECT run_id FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`
		args = append(args, key.keys[2], key.generation)
	case lockRun:
		query = `SELECT id FROM runs WHERE team=? AND id=?`
		mode = tx.forNoKeyUpdate()
	case lockNode:
		query = `SELECT node_id FROM nodes WHERE team=? AND run_id=? AND node_id=?`
		args = append(args, key.keys[2])
	default:
		return errors.New("unsupported work lock tier")
	}
	var id string
	if nonblocking && tx.dialect == DialectPostgres {
		mode += " SKIP LOCKED"
	}
	if err := tx.QueryRowContext(ctx, query+mode, args...).Scan(&id); err != nil {
		if nonblocking && tx.dialect == DialectPostgres && errors.Is(err, sql.ErrNoRows) {
			if err := tx.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
				return err
			}
			return errCapacityAdmissionBusy
		}
		return err
	}
	tx.workLocks = append(tx.workLocks, key)
	return nil
}

// Payment identity is locked after its discovered teams. Revalidation under
// the payment lock restarts the transaction if a binding changed meanwhile.
func lockPaymentTeamsTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) (lockSet, error) {
	var set lockSet
	if err := acquireLockSet(ctx, tx, func() lockSet { set = tx.operator.discoverPaymentTeamsTx(ctx, tx, e); return set }); err != nil {
		return lockSet{}, err
	}
	set = set.normalized()
	return set, nil
}

// safety: PostgreSQL runs concurrent claims and settlements in their own transactions, so a team's balance check and
// the write it guards serialize on the team's lock; SQLite allows one writer and is already serial. The lock key is
// a 32-bit hash of the team, so two teams can share one and serialize; that costs only waiting, never a balance.
func lockLedgerTx(ctx context.Context, tx *storeTx, team Team) (*ledgerTx, error) {
	if team == "" {
		return nil, errors.New("ledger: a ledger lock names its team")
	}
	if err := lockFinancialTeamsTx(ctx, tx, team); err != nil {
		return nil, err
	}
	if tx.dialect == DialectPostgres {
		if _, err := tx.execContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/ledger/"+string(team)); err != nil {
			return nil, err
		}
	}
	tx.ledgerLocked = true
	return &ledgerTx{storeTx: tx, team: team}, nil
}

func (op *Operator) acquireRowLock(ctx context.Context, tx *storeTx, row rowLock) error {
	k := row.key
	var query string
	args := []any{k.keys[0]}
	switch k.tier {
	case lockLedger:
		_, err := lockLedgerTx(ctx, tx, Team(k.keys[0]))
		return err
	case lockTeam:
		if tx.ledgerLocked {
			tx.err = errors.New("claim lock order: teams must precede the ledger")
			return tx.err
		}
		query = "SELECT name FROM teams WHERE name=?"
	case lockAccount:
		query = "SELECT id FROM accounts WHERE id=?"
	case lockMembership:
		query = "SELECT account_id FROM memberships WHERE team=? AND account_id=?"
		args = append(args, k.keys[1])
	case lockAuthority:
		query = "SELECT prefix FROM capabilities WHERE prefix=?"
		if capability.AuthorityKind(k.keys[0]) == capability.AuthoritySession {
			query = "SELECT hash FROM sessions WHERE hash=?"
		}
		args = []any{k.keys[1]}
	case lockInstallation:
		query = "SELECT installation_id FROM github_app_installations WHERE installation_id=?"
	default:
		return errors.New("unsupported authority lock tier")
	}
	mode := tx.forShare()
	if row.write {
		mode = tx.forUpdate()
		// Authority identities never change; their foreign-key key-share locks
		// remain compatible with authority mutations.
		if k.tier == lockAuthority {
			mode = tx.forNoKeyUpdate()
		}
	}
	var id string
	err := tx.QueryRowContext(ctx, query+mode, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if row.optional {
			return nil
		}
		if k.tier == lockTeam {
			return ErrUnknownTeam
		}
		if k.tier == lockAccount {
			return ErrNotFound
		}
		if k.tier == lockMembership {
			return authorityError(apierr.Unauthenticated, "membership is unavailable")
		}
		return errAuthorityChanged
	}
	return err
}

func (op *Operator) discoverInstallationLocks(ctx context.Context, tx *storeTx, set lockSet, installation int64) lockSet {
	if set.err != nil {
		return set
	}
	team, _, err := tx.operator.githubInstallationConnectionTx(ctx, tx, installation)
	if err == nil {
		set.add(lockTeam, true, string(team))
	} else if !errors.Is(err, sql.ErrNoRows) {
		set.err = err
	}
	set.rows = append(set.rows, rowLock{key: storeLockKey{tier: lockInstallation, keys: [3]string{strconv.FormatInt(installation, 10)}}, write: true, optional: true})
	return set
}

// discoverInstallationTeardownLocks adds the Actions credentials an
// installation's teardown revokes to [discoverInstallationLocks].
func (op *Operator) discoverInstallationTeardownLocks(ctx context.Context, tx *storeTx, set lockSet, installation int64) lockSet {
	set = op.discoverInstallationLocks(ctx, tx, set, installation)
	if set.err != nil {
		return set
	}
	team, _, err := op.githubInstallationConnectionTx(ctx, tx, installation)
	if errors.Is(err, sql.ErrNoRows) {
		return set
	}
	if err != nil {
		set.err = err
		return set
	}
	prefixes, err := installationActionsRootsTx(ctx, tx, team, installation)
	if err != nil {
		set.err = err
		return set
	}
	if len(prefixes) == 0 {
		return set
	}
	leaves := make([]capability.AuthorityRef, len(prefixes))
	for i, prefix := range prefixes {
		leaves[i] = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
	}
	roots := discoverAuthorityLocks(ctx, tx, leaves, prefixes...)
	set.rows, set.err = append(set.rows, roots.rows...), roots.err
	return set
}

func (op *Operator) discoverAccountTeams(ctx context.Context, tx *storeTx, set lockSet, account string) lockSet {
	if set.err != nil {
		return set
	}
	set.add(lockAccount, true, account)
	teams, err := tx.queryContext(ctx, `SELECT team FROM memberships WHERE account_id=?`, account)
	if err != nil {
		set.err = err
		return set
	}
	for teams.Next() {
		var team string
		if err := teams.Scan(&team); err != nil {
			set.err = err
			break
		}
		set.add(lockTeam, false, team)
	}
	set.err = errors.Join(set.err, teams.Err(), teams.Close())
	if set.err != nil {
		return set
	}
	return set
}

func (op *Operator) discoverAccountSessionLocks(ctx context.Context, tx *storeTx, set lockSet, account string) lockSet {
	set = tx.operator.discoverAccountTeams(ctx, tx, set, account)
	if set.err != nil {
		return set
	}
	rows, err := tx.queryContext(ctx, `SELECT hash,COALESCE(team,'') FROM sessions WHERE account_id=?`, account)
	if err != nil {
		set.err = err
		return set
	}
	type accountSession struct{ id, team string }
	var sessions []accountSession
	for rows.Next() {
		var session accountSession
		if err := rows.Scan(&session.id, &session.team); err != nil {
			set.err = err
			break
		}
		sessions = append(sessions, session)
	}
	set.err = errors.Join(set.err, rows.Err(), rows.Close())
	if set.err != nil {
		return set
	}
	for _, session := range sessions {
		if session.team != "" {
			set.add(lockTeam, false, session.team)
		}
		set = discoverAuthorityWrite(ctx, tx, set, capability.AuthorityRef{Kind: capability.AuthoritySession, ID: session.id})
	}
	return set
}

func discoverAuthorityWrite(ctx context.Context, tx *storeTx, set lockSet, ref capability.AuthorityRef) lockSet {
	if set.err != nil {
		return set
	}
	chain, err := discoverAuthorityChainTx(ctx, tx, ref)
	depth := len(chain) - 1
	if err != nil {
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.Unauthenticated && failure.Code != apierr.IntegrityFailure {
			set.err = err
			return set
		}
		// An unusable chain cannot have a competing live authority reader, but its
		// surviving rows must still be removable by logout or account deletion.
		depth = 0
	}
	set.rows = append(set.rows, rowLock{key: authorityLockKey(authorityLock{ref: ref, depth: depth}), write: true, optional: true})
	return set
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
// Authority rows use acquireLockSet; other tiers retain their transaction's
// acquisition order. SQLite serializes writers and takes no row locks;
// snapshot reads take none either.

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

// controllerEpochLockSQL is the first lock every PostgreSQL write transaction takes: a share lock on the
// promoted controller's epoch, so a promotion's epoch bump waits for in-flight writes and a superseded
// controller's next transaction sees the new epoch and stops.
const controllerEpochLockSQL = `SELECT convert_from(value,'UTF8')::bigint FROM settings WHERE key='controller_epoch' FOR SHARE`
