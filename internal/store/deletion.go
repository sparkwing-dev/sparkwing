package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// Team deletion states. A deletion is pending from the request until the
// purge removes its operational data, and done after.
const (
	TeamDeletionPending = "pending"
	TeamDeletionDone    = "done"
)

// deletedUserLabel replaces a deleted account's address wherever a row that
// outlives the account names who acted.
const deletedUserLabel = "deleted user"

// Deletion errors. Callers map these onto status codes.
var (
	ErrOnlyTeam = errors.New("store: this is your only team; delete your account instead")
	// ErrAmbiguousAccount reports an address held by more than one account
	// and verified by none, so only an account id can name the one meant.
	ErrAmbiguousAccount = errors.New("store: more than one account holds that address; name the account by id")
	// ErrDeletionLeaseLost reports a purge step taken by a purger that no
	// longer holds the deletion's lease, because another replica took it.
	ErrDeletionLeaseLost = errors.New("store: this purger no longer holds the team deletion's lease")
	// ErrTeamBeingDeleted refuses new checkouts and saved cards after deletion has begun.
	ErrTeamBeingDeleted = errors.New("store: the team is being deleted")
)

// LastOwnerError refuses an account deletion that would leave teams with
// members but no owner. Teams lists them, so the person can hand ownership
// on or delete each team first.
type LastOwnerError struct {
	Teams []TeamInfo
}

func (e *LastOwnerError) Error() string {
	names := make([]string, 0, len(e.Teams))
	for _, t := range e.Teams {
		names = append(names, string(t.Slug))
	}
	return "store: the account is the last owner of teams that have other members: " + strings.Join(names, ", ")
}

// Unwrap lets errors.Is(err, ErrLastOwner) hold for this refusal.
func (e *LastOwnerError) Unwrap() error { return ErrLastOwner }

// TeamDeletion is the durable record of one team's deletion.
type TeamDeletion struct {
	Team        Team
	State       string
	RequestedAt time.Time
	FinishedAt  *time.Time
	Attempts    int
	LastError   string
	// RecheckAt is the final SQL sweep time, or nil after it finishes.
	RecheckAt *time.Time
}

// AccountDeletion reports what deleting an account did.
type AccountDeletion struct {
	AccountID string
	// DeletedTeams are the teams the account was the only member of, now
	// queued for deletion.
	DeletedTeams []Team
	// RevokedPrefixes are the tokens the deletion revoked, for a caller to
	// drop from any cache.
	RevokedPrefixes []string
}

// Deletion closes existing authority without requiring its former membership to remain live.
func (op *Operator) discoverDeletionAuthorityLocks(ctx context.Context, tx *storeTx, set lockSet, where string, args ...any) lockSet {
	rows, err := tx.queryContext(ctx, `SELECT prefix,COALESCE(team,'') FROM capabilities WHERE `+where, args...)
	if err != nil {
		set.err = err
		return set
	}
	type target struct{ prefix, team string }
	var targets []target
	for rows.Next() {
		var row target
		if err := rows.Scan(&row.prefix, &row.team); err != nil {
			set.err = err
			break
		}
		targets = append(targets, row)
	}
	set.err = errors.Join(set.err, rows.Err(), rows.Close())
	if set.err != nil {
		return set
	}
	for _, row := range targets {
		if row.team != "" {
			set.add(lockTeam, true, row.team)
		}
		set = discoverAuthorityWrite(ctx, tx, set, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: row.prefix})
	}
	return set
}

func (op *Operator) discoverDeletionAccountLocks(ctx context.Context, tx *storeTx, set lockSet, account string) lockSet {
	set = tx.operator.discoverAccountSessionLocks(ctx, tx, set, account)
	if set.err != nil {
		return set
	}
	rows, err := tx.queryContext(ctx, `SELECT team FROM memberships WHERE account_id=?`, account)
	if err != nil {
		set.err = err
		return set
	}
	teams, err := scanPrefixes(rows)
	if err != nil {
		set.err = err
		return set
	}
	for _, team := range teams {
		set.rows = append(set.rows, rowLock{key: storeLockKey{tier: lockMembership, keys: [3]string{team, account}}, optional: true})
	}
	return set
}

func discoverTeamDeletionLocks(ctx context.Context, tx *storeTx, set lockSet, team Team) lockSet {
	if set.err != nil {
		return set
	}
	set.add(lockTeam, true, string(team))
	ids, err := (&Tenant{s: tx.store, team: team}).memberIDsTx(ctx, tx)
	if err != nil {
		set.err = err
		return set
	}
	for _, account := range ids {
		set = tx.operator.discoverDeletionAccountLocks(ctx, tx, set, account)
		set.rows = append(set.rows, rowLock{key: storeLockKey{tier: lockMembership, keys: [3]string{string(team), account}}, write: true, optional: true})
	}
	rows, err := tx.queryContext(ctx, `SELECT hash FROM sessions WHERE team=?`, team)
	if err != nil {
		set.err = err
		return set
	}
	sessions, err := scanPrefixes(rows)
	if err != nil {
		set.err = err
		return set
	}
	for _, session := range sessions {
		set = discoverAuthorityWrite(ctx, tx, set, capability.AuthorityRef{Kind: capability.AuthoritySession, ID: session})
	}
	return tx.operator.discoverDeletionAuthorityLocks(ctx, tx, set, `team=?`, team)
}

func (op *Operator) discoverAccountDeletionLocks(ctx context.Context, tx *storeTx, set lockSet, account string) lockSet {
	if set.err != nil {
		return set
	}
	acct, err := accountTx(ctx, tx, account)
	if err != nil {
		set.err = err
		return set
	}
	set = tx.operator.discoverDeletionAccountLocks(ctx, tx, set, account)
	set.add(lockAccount, true, account)
	rows, err := tx.queryContext(ctx, `SELECT team FROM memberships WHERE account_id=?`, account)
	if err != nil {
		set.err = err
		return set
	}
	teams, err := scanPrefixes(rows)
	if err != nil {
		set.err = err
		return set
	}
	for _, team := range teams {
		set.add(lockTeam, true, team)
		set.rows = append(set.rows, rowLock{key: storeLockKey{tier: lockMembership, keys: [3]string{team, account}}, write: true, optional: true})
	}
	sole, _, err := tx.operator.ownedTeamsTx(ctx, tx, account)
	if err != nil {
		set.err = err
		return set
	}
	for _, team := range sole {
		set = discoverTeamDeletionLocks(ctx, tx, set, team)
	}
	names, err := accountPrincipalNamesTx(ctx, tx, acct)
	if err != nil {
		set.err = err
		return set
	}
	for _, name := range names {
		teams, err := tx.operator.grantCreatorTeamsTx(ctx, tx, name)
		if err != nil {
			set.err = err
			return set
		}
		for _, team := range teams {
			set.add(lockTeam, true, team)
		}
	}
	return tx.operator.discoverDeletionAuthorityLocks(ctx, tx, set, `created_by=? OR (subject_kind='account' AND subject_id=?)`, account, account)
}

func (t *Tenant) requestTeamDeletionTx(ctx context.Context, tx *storeTx, now time.Time) (TeamDeletion, []string, error) {
	if err := acquireLockSet(ctx, tx, func() lockSet { return discoverTeamDeletionLocks(ctx, tx, lockSet{}, t.team) }); err != nil {
		return TeamDeletion{}, nil, err
	}
	revoked, err := t.markForDeletionTx(ctx, tx, "", now)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	del, err := teamDeletionTx(ctx, tx, t.team)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	return del, revoked, nil
}

// Authority closes before physical cleanup. Financial records survive both purge passes.
func (t *Tenant) markForDeletionTx(ctx context.Context, tx *storeTx, requestedBy string, now time.Time) ([]string, error) {
	if t.team == DefaultTeam {
		return nil, fmt.Errorf("%w: the default team holds every single-team install's rows", ErrInvalidInput)
	}
	at := now.UTC().UnixNano()
	if _, err := tx.execContext(ctx, `
		INSERT INTO team_deletions (slug, requested_by, requested_at, state, attempts, last_error, finished_at)
		VALUES (?, ?, ?, ?, 0, '', NULL)
		ON CONFLICT (slug) DO UPDATE SET
		    requested_by = excluded.requested_by, requested_at = excluded.requested_at,
		    state = excluded.state, attempts = 0, last_error = '', finished_at = NULL
		WHERE team_deletions.state = ?`,
		string(t.team), requestedBy, at, TeamDeletionPending, TeamDeletionDone); err != nil {
		return nil, fmt.Errorf("team deletion: record: %w", err)
	}
	members, err := t.memberIDsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.execContext(ctx, `DELETE FROM memberships WHERE team = ?`, string(t.team)); err != nil {
		return nil, err
	}
	for _, id := range members {
		if err := t.moveSessionsOffTeamTx(ctx, tx, id, now); err != nil {
			return nil, err
		}
	}
	// safety: a session still here belongs to an account with no other team or
	// predates accounts; either would authenticate into a team being emptied.
	if _, err := tx.execContext(ctx, `DELETE FROM sessions WHERE team = ?`, string(t.team)); err != nil {
		return nil, err
	}
	revoked, err := tx.operator.revokeSelectedCapabilitiesTx(ctx, tx, `team=?`, t.team)
	if err != nil {
		return nil, err
	}
	if _, err := tx.execContext(ctx, `
		UPDATE invitations SET withdrawn_at = ?
		WHERE team = ? AND accepted_at IS NULL AND withdrawn_at IS NULL`, now.UnixNano(), string(t.team)); err != nil {
		return nil, err
	}
	if err := t.cancelActiveRunsTx(ctx, tx, now); err != nil {
		return nil, err
	}
	for _, table := range []string{"cron_schedules", "run_effects"} {
		if _, err := tx.execContext(ctx, `DELETE FROM `+table+` WHERE team = ?`, t.team); err != nil {
			return nil, err
		}
	}
	t.s.localSecrets.Range(func(key, _ any) bool {
		if k := key.(localSecretKey); k.team == t.team {
			tx.finishedLocalSecrets = append(tx.finishedLocalSecrets, k)
		}
		return true
	})
	return revoked, nil
}

func (t *Tenant) memberIDsTx(ctx context.Context, tx *storeTx) (_ []string, err error) {
	rows, err := tx.queryContext(ctx, `SELECT account_id FROM memberships WHERE team = ?`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (op *Operator) revokeSelectedCapabilitiesTx(ctx context.Context, tx *storeTx, where string, args ...any) ([]string, error) {
	rows, err := tx.queryContext(ctx, `SELECT prefix FROM capabilities WHERE (`+where+`) AND (revoked_at IS NULL OR revoked_at>?) ORDER BY prefix`, append(args, tx.now(ctx).UnixNano())...)
	if err != nil {
		return nil, err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return nil, err
	}
	for _, prefix := range prefixes {
		if err := tx.operator.revokeAuthorityTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
			return nil, err
		}
	}
	return prefixes, nil
}

func lockTeamWorkTx(ctx context.Context, tx *storeTx, team Team) error {
	rows, err := tx.queryContext(ctx, `SELECT 'trigger',id,'',0 FROM runs WHERE team=? AND finished_at IS NULL
 UNION ALL SELECT 'node',run_id,node_id,0 FROM nodes WHERE team=?
 UNION ALL SELECT 'eval',run_id,source_node_id,source_generation FROM eval_units WHERE team=?`, team, team, team)
	if err != nil {
		return err
	}
	var units []UnitRef
	for rows.Next() {
		var unit UnitRef
		if err := rows.Scan(&unit.Kind, &unit.RunID, &unit.NodeID, &unit.SourceGeneration); err != nil {
			return errors.Join(err, rows.Close())
		}
		units = append(units, unit)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	return tx.operator.lockUnitsTx(ctx, tx, units)
}

func (t *Tenant) cancelActiveRunsTx(ctx context.Context, tx *storeTx, now time.Time) error {
	if err := lockTeamWorkTx(ctx, tx, t.team); err != nil {
		return err
	}
	rows, err := tx.queryContext(ctx, `SELECT id FROM runs WHERE team=? AND finished_at IS NULL ORDER BY id`, t.team)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	previous := tx.cancelingTeam
	tx.cancelingTeam = t.team
	defer func() { tx.cancelingTeam = previous }()
	for _, id := range ids {
		if _, err := transitionRunTx(ctx, tx, t.team, runstate.RunTransition{RunID: runspec.RunID(id), To: runstate.RunCancelled, Actor: runstate.Actor{Kind: runstate.ActorOperator}, Cause: runstate.RunCancel, Reason: "team_deleted"}); err != nil {
			return err
		}
	}
	return nil
}

const teamDeletionSelect = `SELECT slug, state, requested_at, finished_at, attempts, last_error, recheck_at FROM team_deletions`

func scanTeamDeletion(scan func(...any) error) (TeamDeletion, error) {
	var d TeamDeletion
	var team string
	var requested int64
	var finished, recheck sql.NullInt64
	if err := scan(&team, &d.State, &requested, &finished, &d.Attempts, &d.LastError, &recheck); err != nil {
		return TeamDeletion{}, err
	}
	if recheck.Valid {
		ts := time.Unix(0, recheck.Int64).UTC()
		d.RecheckAt = &ts
	}
	d.Team = Team(team)
	d.RequestedAt = time.Unix(0, requested).UTC()
	if finished.Valid {
		ts := time.Unix(0, finished.Int64).UTC()
		d.FinishedAt = &ts
	}
	return d, nil
}

func teamDeletionTx(ctx context.Context, tx *storeTx, team Team) (TeamDeletion, error) {
	return scanTeamDeletion(tx.QueryRowContext(ctx, teamDeletionSelect+` WHERE slug = ?`, string(team)).Scan)
}

func (s *Store) listTeamDeletions(ctx context.Context, where string, args ...any) (_ []TeamDeletion, err error) {
	rows, err := s.query(ctx, teamDeletionSelect+` WHERE `+where+` ORDER BY requested_at, slug`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []TeamDeletion
	for rows.Next() {
		d, err := scanTeamDeletion(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TeamDeletionsRequestedBy lists the deletions an account asked for, so the
// requester can watch one it no longer has a team to look from.
func (o *Operator) TeamDeletionsRequestedBy(ctx context.Context, accountID string) ([]TeamDeletion, error) {
	if accountID == "" {
		return nil, nil
	}
	return o.s.listTeamDeletions(ctx, `requested_by = ?`, accountID)
}

// PendingTeamDeletions lists every deletion with work left: those the purge
// has not finished, and finished ones still owed their recheck. Oldest first.
func (o *Operator) PendingTeamDeletions(ctx context.Context) ([]TeamDeletion, error) {
	return o.s.listTeamDeletions(ctx, `state = ? OR recheck_at IS NOT NULL`, TeamDeletionPending)
}

// ClaimTeamDeletion takes, or renews, holder's lease on a team deletion
// that has work due at now, for lease. It reports false while another
// holder's lease is live or nothing is due, so one replica purges a team at
// a time; a purger calls it again before each step that reaches outside the
// database, and stops when it reports false.
func (o *Operator) ClaimTeamDeletion(ctx context.Context, team Team, holder string, lease time.Duration) (out bool, err error) {
	err = o.s.inTxOnce(ctx, func(tx *storeTx) error {
		if holder == "" {
			return errors.New("store: a deletion lease needs a holder")
		}
		at := tx.now(ctx).UTC().UnixNano()
		res, err := tx.execContext(ctx, `
			UPDATE team_deletions SET lease_holder = ?, lease_until = ?
			WHERE slug = ?
			  AND (state = ? OR (state = ? AND recheck_at IS NOT NULL AND recheck_at <= ?))
			  AND (lease_until IS NULL OR lease_until < ? OR lease_holder = ?)`,
			holder, tx.now(ctx).Add(lease).UTC().UnixNano(), string(NormalizeTeam(team)),
			TeamDeletionPending, TeamDeletionDone, at, at, holder)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		out = n == 1
		return err
	})
	return out, err
}

func requireDeletionLeaseTx(ctx context.Context, tx *storeTx, team Team, holder, state string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM team_deletions
		WHERE slug = ? AND state = ? AND lease_holder = ? AND lease_until >= ?`,
		string(team), state, holder, tx.now(ctx).UTC().UnixNano()).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: %s", ErrDeletionLeaseLost, team)
	}
	return nil
}

// Run dependents go first to avoid a large cascade; requirement sets follow
// the work rows that reference them.
func deleteTenantRowsTx(ctx context.Context, tx *storeTx, team Team) error {
	tables := []string{"capabilities", "sessions"}
	for _, table := range schemaTables {
		if table.Owner == schemaTenant && !table.RetainOnTeamDeletion {
			tables = append(tables, table.Name)
		}
	}
	for _, table := range append(slices.DeleteFunc(tables, func(t string) bool {
		return t == "runs" || t == "quota_admission" || t == "claim_requirements" || t == "claim_contexts"
	}), "quota_admission", "runs", "claim_contexts", "claim_requirements") {
		query := `DELETE FROM ` + table + ` WHERE team = ?`
		if _, err := tx.execContext(ctx, query, string(team)); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	return nil
}

// PurgeTeam removes operational data and clears the team to a tombstone.
// Financial records keep their payer association for late callbacks.
// holder must hold the deletion lease; see [Operator.ClaimTeamDeletion].
func (o *Operator) PurgeTeam(ctx context.Context, team Team, holder string, recheckAfter time.Duration) error {
	team = NormalizeTeam(team)
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := requireDeletionLeaseTx(ctx, tx, team, holder, TeamDeletionPending); err != nil {
			return err
		}
		if err := lockFinancialTeamsTx(ctx, tx, team); err != nil {
			return err
		}
		if err := settleTeamAwardsTx(ctx, tx, team); err != nil {
			return err
		}
		if err := deleteTenantRowsTx(ctx, tx, team); err != nil {
			return err
		}
		at := tx.now(ctx).UTC().UnixNano()
		res, err := tx.execContext(ctx, `
			UPDATE team_deletions SET state = ?, finished_at = ?, recheck_at = ?, last_error = '',
			    lease_holder = '', lease_until = NULL
			WHERE slug = ? AND state = ? AND lease_holder = ?`,
			TeamDeletionDone, at, tx.now(ctx).Add(recheckAfter).UTC().UnixNano(), string(team), TeamDeletionPending, holder)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errors.Join(fmt.Errorf("%w: %s", ErrDeletionLeaseLost, team), err)
		}
		if _, err := tx.execContext(ctx, `UPDATE teams SET display_name='', created_by='', members_can_add_machines=1,
		    claim_ranking='', max_children_per_root=50, high_trust=0, claim_award_ordinal=0,
		    billing_trust_at=0, billing_trust_reason='', billing_trust='', billing_trust_by='',
		    card_customer='', card_added_at=0,
		    refill_enabled=0, refill_threshold_cents=0, refill_amount_cents=0, refill_week_limit_cents=0, refill_month_limit_cents=0,
		    next_run_number=1 WHERE name = ?`, string(team)); err != nil {
			return err
		}
		if _, err := tx.execContext(ctx,
			`UPDATE accounts SET active_team = '', updated_at = ? WHERE active_team = ?`, at, string(team)); err != nil {
			return err
		}
		return nil
	})
}

// FinishTeamRecheck performs the final SQL sweep under the holder's deletion lease.
func (o *Operator) FinishTeamRecheck(ctx context.Context, team Team, holder string) error {
	team = NormalizeTeam(team)
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := requireDeletionLeaseTx(ctx, tx, team, holder, TeamDeletionDone); err != nil {
			return err
		}
		if err := lockFinancialTeamsTx(ctx, tx, team); err != nil {
			return err
		}
		if err := deleteTenantRowsTx(ctx, tx, team); err != nil {
			return err
		}
		res, err := tx.execContext(ctx, `
		UPDATE team_deletions SET recheck_at = NULL, lease_holder = '', lease_until = NULL, last_error = ''
		WHERE slug = ? AND state = ? AND lease_holder = ?`, string(team), TeamDeletionDone, holder)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errors.Join(fmt.Errorf("%w: %s", ErrDeletionLeaseLost, team), err)
		}
		return nil
	})
}

// RecordTeamDeletionFailure notes a purge attempt that failed, so the
// deletion's state shows why it has not finished.
func (o *Operator) RecordTeamDeletionFailure(ctx context.Context, team Team, cause string) error {
	_, err := o.s.exec(ctx, `
		UPDATE team_deletions SET attempts = attempts + 1, last_error = ?
		WHERE slug = ? AND (state = ? OR recheck_at IS NOT NULL)`, truncate(cause, 500), string(NormalizeTeam(team)), TeamDeletionPending)
	return err
}

// AccountByEmail finds the account an address names: the one holding it
// verified, or the only one holding it at all.
func (s *Store) AccountByEmail(ctx context.Context, email string) (_ Account, err error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return Account{}, err
	}
	defer rollbackOrLog(tx)
	out, err := accountByEmailTx(ctx, tx, email)
	if err != nil {
		return Account{}, err
	}
	return out, tx.Commit()
}

// DeleteAccount removes an account and everything that identifies it: its
// identities, memberships, sessions and account row. Tokens it minted in any
// team are revoked. Teams it was the only member of are queued for deletion
// with all their data. Rows that belong to a team and name the account, such
// as the runs it started, stay with the team and name [deletedUserLabel]
// instead.
//
// It refuses with a [*LastOwnerError] while the account is the last owner of
// a team that has other members, because deleting it would leave those
// members in a team nobody can administer.
func (s *Store) DeleteAccount(ctx context.Context, accountID string) (AccountDeletion, error) {
	var result AccountDeletion
	err := s.inTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = tx.operator.deleteAccountTx(ctx, tx, accountID, tx.now(ctx))
		return err
	})
	return result, err
}

func (o *Operator) deleteAccountTx(ctx context.Context, tx *storeTx, accountID string, now time.Time) (AccountDeletion, error) {
	if err := acquireLockSet(ctx, tx, func() lockSet { return tx.operator.discoverAccountDeletionLocks(ctx, tx, lockSet{}, accountID) }); err != nil {
		return AccountDeletion{}, err
	}
	acct, err := accountTx(ctx, tx, accountID)
	if err != nil {
		return AccountDeletion{}, err
	}
	names, err := accountPrincipalNamesTx(ctx, tx, acct)
	if err != nil {
		return AccountDeletion{}, err
	}
	sole, blocked, err := tx.operator.ownedTeamsTx(ctx, tx, accountID)
	if err != nil {
		return AccountDeletion{}, err
	}
	if len(blocked) > 0 {
		return AccountDeletion{}, &LastOwnerError{Teams: blocked}
	}
	res := AccountDeletion{AccountID: accountID}
	for _, team := range sole {
		if err := lockTeamWorkTx(ctx, tx, team); err != nil {
			return AccountDeletion{}, err
		}
	}
	for _, team := range sole {
		t := &Tenant{s: o.s, team: team}
		revoked, err := t.markForDeletionTx(ctx, tx, accountID, now)
		if err != nil {
			return AccountDeletion{}, err
		}
		res.DeletedTeams = append(res.DeletedTeams, team)
		res.RevokedPrefixes = append(res.RevokedPrefixes, revoked...)
	}
	// safety: a mint reads its minter's role under the membership row's lock,
	// so dropping the memberships before sweeping the account's tokens makes
	// a mint in flight either finish before the sweep reads or find no role.
	if _, err := tx.execContext(ctx, `DELETE FROM memberships WHERE account_id = ?`, accountID); err != nil {
		return AccountDeletion{}, fmt.Errorf("delete account: %w", err)
	}
	minted, err := tx.operator.revokeSelectedCapabilitiesTx(ctx, tx, `created_by=? OR (subject_kind='account' AND subject_id=?)`, accountID, accountID)
	if err != nil {
		return AccountDeletion{}, err
	}
	res.RevokedPrefixes = append(res.RevokedPrefixes, minted...)
	for _, name := range names {
		if err := tx.operator.relabelPrincipalTx(ctx, tx, name); err != nil {
			return AccountDeletion{}, fmt.Errorf("delete account: %w", err)
		}
	}
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE capabilities SET created_by='' WHERE created_by=?`, []any{accountID}},
		{`DELETE FROM run_control_requests WHERE subject_kind='account' AND subject_id=?`, []any{accountID}},
		{`DELETE FROM sessions WHERE account_id = ?`, []any{accountID}},
		{`DELETE FROM identities WHERE account_id = ?`, []any{accountID}},
		{`DELETE FROM identity_unlinks WHERE account_id = ?`, []any{accountID}},
		{`DELETE FROM invitations WHERE email = ?`, []any{acct.Email}},
		{`UPDATE invitations SET withdrawn_at = ? WHERE invited_by = ? AND accepted_at IS NULL AND withdrawn_at IS NULL`, []any{now.UnixNano(), accountID}},
		{`UPDATE invitations SET invited_by = '' WHERE invited_by = ?`, []any{accountID}},
		{`UPDATE invitations SET accepted_by = '' WHERE accepted_by = ?`, []any{accountID}},
		{`UPDATE github_runner_bindings SET created_by = '' WHERE created_by = ?`, []any{accountID}},
		{`UPDATE teams SET created_by = '' WHERE created_by = ?`, []any{accountID}},
		{`UPDATE team_deletions SET requested_by = '' WHERE requested_by = ?`, []any{accountID}},
		{`UPDATE standing_requests SET requested_by = '' WHERE requested_by = ?`, []any{accountID}},
		{`UPDATE standing_requests SET decided_by = '' WHERE decided_by = ?`, []any{accountID}},
		{`DELETE FROM accounts WHERE id = ?`, []any{accountID}},
	} {
		if _, err := tx.execContext(ctx, stmt.sql, stmt.args...); err != nil {
			return AccountDeletion{}, fmt.Errorf("delete account: %w", err)
		}
	}
	return res, nil
}

func (op *Operator) ownedTeamsTx(ctx context.Context, tx *storeTx, accountID string) (sole []Team, blocked []TeamInfo, err error) {
	rows, err := tx.queryContext(ctx, `
		SELECT m.team, t.display_name,
		       (SELECT COUNT(*) FROM memberships o WHERE o.team = m.team AND o.account_id <> m.account_id AND o.role = ?),
		       (SELECT COUNT(*) FROM memberships o WHERE o.team = m.team AND o.account_id <> m.account_id)
		FROM memberships m JOIN teams t ON t.name = m.team
		WHERE m.account_id = ? AND m.role = ?
		ORDER BY m.team`, string(RoleOwner), accountID, string(RoleOwner))
	if err != nil {
		return nil, nil, err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var team, display string
		var otherOwners, otherMembers int
		if err := rows.Scan(&team, &display, &otherOwners, &otherMembers); err != nil {
			return nil, nil, err
		}
		switch {
		case otherOwners > 0:
		case otherMembers == 0:
			sole = append(sole, Team(team))
		default:
			blocked = append(blocked, TeamInfo{Slug: Team(team), DisplayName: display})
		}
	}
	return sole, blocked, rows.Err()
}

type principalName struct {
	name string
	team string
}

func accountPrincipalNamesTx(ctx context.Context, tx *storeTx, acct Account) (_ []principalName, err error) {
	names := []principalName{{name: acct.Email}, {name: acct.ID}}
	add := func(n principalName) {
		if n.name != "" && n.name != deletedUserLabel && !slices.Contains(names, n) &&
			!slices.Contains(names, principalName{name: n.name}) {
			names = append(names, n)
		}
	}
	rows, err := tx.queryContext(ctx, `
		SELECT email, '' FROM identities WHERE account_id = ? AND linked = 0`, acct.ID)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var n principalName
		if err := rows.Scan(&n.name, &n.team); err != nil {
			return nil, err
		}
		add(n)
	}
	return names, rows.Err()
}

func (op *Operator) relabelPrincipalTx(ctx context.Context, tx *storeTx, n principalName) error {
	label := deletedUserLabel
	scope, args := "", []any{label, n.name}
	if n.team != "" {
		scope, args = ` AND team = ?`, append(args, n.team)
	}
	for _, col := range []struct{ table, column string }{
		{"runs", "created_principal"},
		{"triggers", "claim_principal"},
		{"approvals", "approver"},
		{"cron_schedules", "armed_by"},
		{"debug_pauses", "released_by"},
		{"nodes", "claim_principal"},
		{"secrets", "principal"},
	} {
		if _, err := tx.execContext(ctx,
			`UPDATE `+col.table+` SET `+col.column+` = ? WHERE `+col.column+` = ?`+scope, args...); err != nil {
			return fmt.Errorf("relabel %s.%s: %w", col.table, col.column, err)
		}
	}
	if err := relabelGrantCreatorTx(ctx, tx, n, label); err != nil {
		return err
	}
	eventsSQL := `UPDATE events SET payload = CAST(REPLACE(CAST(payload AS TEXT), ?, ?) AS BLOB)
		WHERE payload IS NOT NULL AND instr(CAST(payload AS TEXT), ?) > 0`
	if tx.dialect == DialectPostgres {
		eventsSQL = `UPDATE events SET payload = convert_to(replace(convert_from(payload, 'UTF8'), ?, ?), 'UTF8')
		WHERE payload IS NOT NULL AND strpos(convert_from(payload, 'UTF8'), ?) > 0`
	}
	eventArgs := []any{n.name, label, n.name}
	if n.team != "" {
		eventsSQL += ` AND team = ?`
		eventArgs = append(eventArgs, n.team)
	}
	if _, err := tx.execContext(ctx, eventsSQL, eventArgs...); err != nil {
		return fmt.Errorf("relabel event payloads: %w", err)
	}
	return nil
}

// safety: a relabel can reach grants on teams the account does not own, so it takes each touched team's ledger lock
// in name order before rewriting that team's grants.
func relabelGrantCreatorTx(ctx context.Context, tx *storeTx, n principalName, label string) error {
	teams, err := tx.operator.grantCreatorTeamsTx(ctx, tx, n)
	if err != nil {
		return err
	}
	for _, team := range teams {
		l, err := lockLedgerTx(ctx, tx, Team(team))
		if err != nil {
			return err
		}
		if err := l.relabelCreator(ctx, n.name, label); err != nil {
			return fmt.Errorf("relabel credit_grants.created_by: %w", err)
		}
	}
	return nil
}

func (op *Operator) grantCreatorTeamsTx(ctx context.Context, tx *storeTx, n principalName) ([]string, error) {
	query, args := `SELECT DISTINCT team FROM credit_grants WHERE created_by=?`, []any{n.name}
	if n.team != "" {
		query, args = query+` AND team=?`, append(args, n.team)
	}
	rows, err := tx.queryContext(ctx, query+` ORDER BY team`, args...)
	if err != nil {
		return nil, err
	}
	return scanPrefixes(rows)
}

// MaxInvitationEmailsPerDay bounds how many invitation emails one address
// receives in a day from every team together, so no set of teams can use the
// deployment's sender to flood an inbox.
const MaxInvitationEmailsPerDay = 5

// safety: a send counts toward the cap for this window, and the retention pass drops each log row after it.
const invitationEmailWindow = 24 * time.Hour

// ClaimInvitationEmail records that an invitation is about to be mailed, and
// reports false instead when its address has already been mailed
// [MaxInvitationEmailsPerDay] times in the last 24 hours, counting every
// team. A claimed invitation is never claimed twice.
func (o *Operator) ClaimInvitationEmail(ctx context.Context, invitationID string) (bool, error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	var email string
	err = tx.QueryRowContext(ctx, `SELECT email FROM invitations WHERE id = ?`, invitationID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	// safety: two teams inviting one address at once would each count the
	// other's send as not yet made; the lock orders them. SQLite already
	// serializes every writer.
	address := invitationEmailKey(email)
	if tx.dialect == DialectPostgres {
		if _, err := tx.execContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/invitation-email/"+address); err != nil {
			return false, err
		}
	}
	at := tx.now(ctx).UTC().UnixNano()
	since := tx.now(ctx).Add(-invitationEmailWindow).UnixNano()
	var sent int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM invitation_email_log WHERE address = ? AND sent_at > ?`, address, since).Scan(&sent); err != nil {
		return false, err
	}
	if sent >= MaxInvitationEmailsPerDay {
		return false, nil
	}
	res, err := tx.execContext(ctx,
		`UPDATE invitations SET emailed_at = ? WHERE id = ? AND emailed_at IS NULL`, at, invitationID)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.execContext(ctx,
		`INSERT INTO invitation_email_log (address, sent_at) VALUES (?, ?)`, address, at); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// safety: the log outlives the invitation and the team that sent it, so it
// keeps a digest of the address rather than the address; the cap still
// counts one inbox however its case was typed, because the address is
// normalized before it is hashed.
func invitationEmailKey(email string) string {
	sum := sha256.Sum256([]byte(normalizeEmail(email)))
	return hex.EncodeToString(sum[:])
}

func accountByEmailTx(ctx context.Context, tx *storeTx, email string) (_ Account, err error) {
	rows, err := tx.queryContext(ctx, accountSelect+` WHERE email = ? ORDER BY email_verified DESC, created_at`, normalizeEmail(email))
	if err != nil {
		return Account{}, err
	}
	defer closeRowsInto(rows, &err)
	var found []Account
	for rows.Next() {
		var a Account
		var verified int
		var active string
		var created, waitlisted int64
		if err := rows.Scan(&a.ID, &a.Email, &verified, &a.Name, &active, &created, &waitlisted); err != nil {
			return Account{}, err
		}
		a.EmailVerified, a.ActiveTeam, a.CreatedAt = verified == 1, Team(active), time.Unix(0, created).UTC()
		a.Waitlisted = waitlisted != 0
		found = append(found, a)
	}
	if err := rows.Err(); err != nil {
		return Account{}, err
	}
	switch {
	case len(found) == 0:
		return Account{}, ErrNotFound
	case found[0].EmailVerified || len(found) == 1:
		return found[0], nil
	default:
		return Account{}, ErrAmbiguousAccount
	}
}
