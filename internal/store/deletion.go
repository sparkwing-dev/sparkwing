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
)

// Team deletion states. A deletion is pending from the request until the
// purge removes the team's last row, and done after.
const (
	TeamDeletionPending = "pending"
	TeamDeletionDone    = "done"
)

// DeletedUserLabel replaces a deleted account's address wherever a row that
// outlives the account names who acted.
const DeletedUserLabel = "deleted user"

// Deletion errors. Callers map these onto status codes.
var (
	ErrOnlyTeam = errors.New("store: this is your only team; delete your account instead")
	// ErrAmbiguousAccount reports an address held by more than one account
	// and verified by none, so only an account id can name the one meant.
	ErrAmbiguousAccount = errors.New("store: more than one account holds that address; name the account by id")
	// ErrDeletionLeaseLost reports a purge step taken by a purger that no
	// longer holds the deletion's lease, because another replica took it.
	ErrDeletionLeaseLost = errors.New("store: this purger no longer holds the team deletion's lease")
	// ErrOpenCheckout refuses deleting a team with an unexpired, unpaid
	// checkout, because its payment would land after the team is gone.
	ErrOpenCheckout = errors.New("store: the team has a credit checkout in progress; " +
		"wait for the checkout to expire or complete, then delete the team")
	// ErrTeamFrozen refuses deleting a team held over a disputed payment,
	// because the hold and the ledger it points at are the dispute's record.
	ErrTeamFrozen = errors.New("store: the team is on hold over a disputed payment; contact support to delete it")
	// ErrTeamBeingDeleted refuses opening a checkout for a team whose deletion
	// has begun, since the payment would land after the purge.
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

// RequestTeamDeletion queues a team for deletion on the operator's word,
// with no membership check, for a deletion request that arrived by mail.
func (o *Operator) RequestTeamDeletion(ctx context.Context, team Team, now time.Time) (TeamDeletion, []string, error) {
	t := &Tenant{s: o.s, team: NormalizeTeam(team)}
	var result TeamDeletion
	var revoked []string
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		result, revoked, err = t.requestTeamDeletionTx(ctx, tx, now)
		return err
	})
	return result, revoked, err
}

func (t *Tenant) requestTeamDeletionTx(ctx context.Context, tx *storeTx, now time.Time) (TeamDeletion, []string, error) {
	if _, err := lockLedgerTx(ctx, tx, t.team); err != nil {
		return TeamDeletion{}, nil, err
	}
	if err := t.lockTeamTx(ctx, tx); err != nil {
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

// safety: a payment that completes after the purge finds no team to credit,
// and a hold's rows are the dispute's record, so either keeps the team until
// it clears.
func refuseDeletionWithMoneyInFlightTx(ctx context.Context, tx *storeTx, team Team, now time.Time) error {
	var open int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM credit_checkouts WHERE team = ? AND paid_at IS NULL AND expires_at > ?`,
		string(team), now.UnixNano()).Scan(&open); err != nil {
		return err
	}
	if open > 0 {
		return ErrOpenCheckout
	}
	freeze, err := teamCreditFreezeTx(ctx, tx, team)
	if err != nil {
		return err
	}
	if freeze.Frozen {
		return ErrTeamFrozen
	}
	return nil
}

// safety: every way into the team closes before any row is removed; the rows go later, in
// [Operator.PurgeTeam], because they may not fit one transaction. Physical logs and blobs are
// removed separately by the controller's log cleanup and CAS collection.
func (t *Tenant) markForDeletionTx(ctx context.Context, tx *storeTx, requestedBy string, now time.Time) ([]string, error) {
	if t.team == DefaultTeam {
		return nil, fmt.Errorf("%w: the default team holds every single-team install's rows", ErrInvalidInput)
	}
	if err := refuseDeletionWithMoneyInFlightTx(ctx, tx, t.team, now); err != nil {
		return nil, err
	}
	at := now.UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM memberships WHERE team = ?`, string(t.team)); err != nil {
		return nil, err
	}
	for _, id := range members {
		if err := t.moveSessionsOffTeamTx(ctx, tx, id, now); err != nil {
			return nil, err
		}
	}
	// safety: a session still here belongs to an account with no other team or
	// predates accounts; either would authenticate into a team being emptied.
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE team = ?`, string(t.team)); err != nil {
		return nil, err
	}
	revoked, err := revokeSelectedCapabilitiesTx(ctx, tx, `team=?`, t.team)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE invitations SET withdrawn_at = ?
		WHERE team = ? AND accepted_at IS NULL AND withdrawn_at IS NULL`, now.UnixNano(), string(t.team)); err != nil {
		return nil, err
	}
	if err := t.cancelActiveRunsTx(ctx, tx, now); err != nil {
		return nil, err
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
	rows, err := tx.QueryContext(ctx, `SELECT account_id FROM memberships WHERE team = ?`, string(t.team))
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

func revokeSelectedCapabilitiesTx(ctx context.Context, tx *storeTx, where string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE (`+where+`) AND (revoked_at IS NULL OR revoked_at>?) ORDER BY prefix`, append(args, tx.now().UnixNano())...)
	if err != nil {
		return nil, err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return nil, err
	}
	for _, prefix := range prefixes {
		if err := revokeAuthorityTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
			return nil, err
		}
	}
	return prefixes, nil
}

func (t *Tenant) cancelActiveRunsTx(ctx context.Context, tx *storeTx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM runs WHERE team=? AND finished_at IS NULL ORDER BY id`, t.team)
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
		if _, err := transitionRunTx(ctx, tx, t.team, RunTransition{RunID: id, To: RunCancelled, Actor: Actor{Kind: ActorOperator}, Cause: RunCancel, Reason: "team_deleted"}, now); err != nil {
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
func (s *Store) TeamDeletionsRequestedBy(ctx context.Context, accountID string) ([]TeamDeletion, error) {
	if accountID == "" {
		return nil, nil
	}
	return s.listTeamDeletions(ctx, `requested_by = ?`, accountID)
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
func (o *Operator) ClaimTeamDeletion(ctx context.Context, team Team, holder string, now time.Time, lease time.Duration) (bool, error) {
	if holder == "" {
		return false, errors.New("store: a deletion lease needs a holder")
	}
	at := now.UTC().UnixNano()
	res, err := o.s.exec(ctx, `
		UPDATE team_deletions SET lease_holder = ?, lease_until = ?
		WHERE slug = ?
		  AND (state = ? OR (state = ? AND recheck_at IS NOT NULL AND recheck_at <= ?))
		  AND (lease_until IS NULL OR lease_until < ? OR lease_holder = ?)`,
		holder, now.Add(lease).UTC().UnixNano(), string(NormalizeTeam(team)),
		TeamDeletionPending, TeamDeletionDone, at, at, holder)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (o *Operator) requireLease(ctx context.Context, team Team, holder, state string, now time.Time) error {
	var n int
	if err := o.s.queryRow(ctx, `
		SELECT COUNT(*) FROM team_deletions
		WHERE slug = ? AND state = ? AND lease_holder = ? AND lease_until >= ?`,
		string(team), state, holder, now.UTC().UnixNano()).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: %s", ErrDeletionLeaseLost, team)
	}
	return nil
}

// safety: runs goes last so the rows that reference a run are removed by
// their own statements rather than by a cascade from a very large delete.
func (o *Operator) deleteTenantRows(ctx context.Context, team Team) error {
	tables := append([]string{"business_events", "capabilities", "sessions", "payment_events"}, tenantTables...)
	tables = slices.DeleteFunc(tables, func(t string) bool { return t == "credit_grants" || t == "credit_charges" })
	for _, table := range append(slices.DeleteFunc(tables, func(t string) bool { return t == "runs" || t == "quota_admission" }), "quota_admission", "runs") {
		if _, err := o.s.exec(ctx, `DELETE FROM `+table+` WHERE team = ?`, string(team)); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		l, err := lockLedgerTx(ctx, tx, team)
		if err != nil {
			return err
		}
		return l.purge(ctx)
	})
}

// PurgeTeam removes every row a team owns in every tenant-owned table, then
// its registry row, and marks its deletion done with a recheck due at
// now+recheckAfter. holder must hold the deletion's lease; see
// [Operator.ClaimTeamDeletion]. Each table is its own statement rather than
// one transaction, because a large team's rows may not fit one; a purge
// that stops part-way leaves the deletion pending and the next one finishes
// it.
func (o *Operator) PurgeTeam(ctx context.Context, team Team, holder string, now time.Time, recheckAfter time.Duration) error {
	team = NormalizeTeam(team)
	if err := o.requireLease(ctx, team, holder, TeamDeletionPending, now); err != nil {
		return err
	}
	if err := o.deleteTenantRows(ctx, team); err != nil {
		return err
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		at := now.UTC().UnixNano()
		res, err := tx.ExecContext(ctx, `
			UPDATE team_deletions SET state = ?, finished_at = ?, recheck_at = ?, last_error = '',
			    lease_holder = '', lease_until = NULL
			WHERE slug = ? AND state = ? AND lease_holder = ?`,
			TeamDeletionDone, at, now.Add(recheckAfter).UTC().UnixNano(), string(team), TeamDeletionPending, holder)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errors.Join(fmt.Errorf("%w: %s", ErrDeletionLeaseLost, team), err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM teams WHERE name = ?`, string(team)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE accounts SET active_team = '', updated_at = ? WHERE active_team = ?`, at, string(team)); err != nil {
			return err
		}
		return nil
	})
}

// FinishTeamRecheck performs the final SQL sweep under the holder's deletion lease.
func (o *Operator) FinishTeamRecheck(ctx context.Context, team Team, holder string, now time.Time) error {
	team = NormalizeTeam(team)
	if err := o.requireLease(ctx, team, holder, TeamDeletionDone, now); err != nil {
		return err
	}
	if err := o.deleteTenantRows(ctx, team); err != nil {
		return err
	}
	res, err := o.s.exec(ctx, `
		UPDATE team_deletions SET recheck_at = NULL, lease_holder = '', lease_until = NULL, last_error = ''
		WHERE slug = ? AND state = ? AND lease_holder = ?`, string(team), TeamDeletionDone, holder)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(fmt.Errorf("%w: %s", ErrDeletionLeaseLost, team), err)
	}
	return nil
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
// as the runs it started, stay with the team and name [DeletedUserLabel]
// instead.
//
// It refuses with a [*LastOwnerError] while the account is the last owner of
// a team that has other members, because deleting it would leave those
// members in a team nobody can administer.
func (s *Store) DeleteAccount(ctx context.Context, accountID string, now time.Time) (AccountDeletion, error) {
	var result AccountDeletion
	err := s.inTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = s.deleteAccountTx(ctx, tx, accountID, now)
		return err
	})
	return result, err
}

func (s *Store) deleteAccountTx(ctx context.Context, tx *storeTx, accountID string, now time.Time) (AccountDeletion, error) {
	acct, err := accountTx(ctx, tx, accountID)
	if err != nil {
		return AccountDeletion{}, err
	}
	names, err := accountPrincipalNamesTx(ctx, tx, acct)
	if err != nil {
		return AccountDeletion{}, err
	}
	if err := lockOwnedTeamsTx(ctx, tx, s, accountID, names); err != nil {
		return AccountDeletion{}, err
	}
	sole, blocked, err := ownedTeamsTx(ctx, tx, accountID)
	if err != nil {
		return AccountDeletion{}, err
	}
	if len(blocked) > 0 {
		return AccountDeletion{}, &LastOwnerError{Teams: blocked}
	}
	res := AccountDeletion{AccountID: accountID}
	for _, team := range sole {
		t := &Tenant{s: s, team: team}
		if err := t.lockTeamTx(ctx, tx); err != nil {
			return AccountDeletion{}, err
		}
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM memberships WHERE account_id = ?`, accountID); err != nil {
		return AccountDeletion{}, fmt.Errorf("delete account: %w", err)
	}
	minted, err := revokeSelectedCapabilitiesTx(ctx, tx, `created_by=? OR (subject_kind='account' AND subject_id=?)`, accountID, accountID)
	if err != nil {
		return AccountDeletion{}, err
	}
	res.RevokedPrefixes = append(res.RevokedPrefixes, minted...)
	for _, name := range names {
		if err := relabelPrincipalTx(ctx, tx, name); err != nil {
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
		{`DELETE FROM business_events WHERE team IS NULL AND account = ?`, []any{accountID}},
		{`UPDATE business_events SET account = '' WHERE account = ?`, []any{accountID}},
		{`UPDATE business_events SET actor = '' WHERE actor IN (?, ?)`, []any{accountID, acct.Email}},
		{`DELETE FROM accounts WHERE id = ?`, []any{accountID}},
	} {
		if _, err := tx.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return AccountDeletion{}, fmt.Errorf("delete account: %w", err)
		}
	}
	return res, nil
}

func ownedTeamsTx(ctx context.Context, tx *storeTx, accountID string) (sole []Team, blocked []TeamInfo, err error) {
	rows, err := tx.QueryContext(ctx, `
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

// safety: two co-owners deleting their accounts at once would each count the
// other as the owner who stays; locking every team the account owns first,
// in one order, makes the second count see the first's departure without
// two deletions deadlocking on each other's teams.
func lockOwnedTeamsTx(ctx context.Context, tx *storeTx, s *Store, accountID string, names []principalName) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT team FROM memberships WHERE account_id = ? AND role = ? ORDER BY team`, accountID, string(RoleOwner))
	if err != nil {
		return err
	}
	teams, err := scanPrefixes(rows)
	if err != nil {
		return err
	}
	ledgerTeams := slices.Clone(teams)
	for _, name := range names {
		touched, err := grantCreatorTeamsTx(ctx, tx, name)
		if err != nil {
			return err
		}
		ledgerTeams = append(ledgerTeams, touched...)
	}
	slices.Sort(ledgerTeams)
	// safety: take all touched ledger locks before any team row, in one order, including teams the account
	// does not own, so cross-team relabeling cannot invert another account deletion's locks.
	for _, team := range slices.Compact(ledgerTeams) {
		if _, err := lockLedgerTx(ctx, tx, Team(team)); err != nil {
			return err
		}
	}
	for _, team := range teams {
		if err := (&Tenant{s: s, team: Team(team)}).lockTeamTx(ctx, tx); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

type principalName struct {
	name string
	team string
}

func accountPrincipalNamesTx(ctx context.Context, tx *storeTx, acct Account) (_ []principalName, err error) {
	names := []principalName{{name: acct.Email}, {name: acct.ID}}
	add := func(n principalName) {
		if n.name != "" && n.name != DeletedUserLabel && !slices.Contains(names, n) &&
			!slices.Contains(names, principalName{name: n.name}) {
			names = append(names, n)
		}
	}
	rows, err := tx.QueryContext(ctx, `
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

func relabelPrincipalTx(ctx context.Context, tx *storeTx, n principalName) error {
	label := DeletedUserLabel
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
		if _, err := tx.ExecContext(ctx,
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
	if _, err := tx.ExecContext(ctx, eventsSQL, eventArgs...); err != nil {
		return fmt.Errorf("relabel event payloads: %w", err)
	}
	return nil
}

// safety: a relabel can reach grants on teams the account does not own, so it takes each touched team's ledger lock
// in name order before rewriting that team's grants.
func relabelGrantCreatorTx(ctx context.Context, tx *storeTx, n principalName, label string) error {
	teams, err := grantCreatorTeamsTx(ctx, tx, n)
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

func grantCreatorTeamsTx(ctx context.Context, tx *storeTx, n principalName) ([]string, error) {
	query, args := `SELECT DISTINCT team FROM credit_grants WHERE created_by=?`, []any{n.name}
	if n.team != "" {
		query, args = query+` AND team=?`, append(args, n.team)
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY team`, args...)
	if err != nil {
		return nil, err
	}
	return scanPrefixes(rows)
}

// MaxInvitationEmailsPerDay bounds how many invitation emails one address
// receives in a day from every team together, so no set of teams can use the
// deployment's sender to flood an inbox.
const MaxInvitationEmailsPerDay = 5

// ClaimInvitationEmail records that an invitation is about to be mailed, and
// reports false instead when its address has already been mailed
// [MaxInvitationEmailsPerDay] times in the last 24 hours, counting every
// team. A claimed invitation is never claimed twice.
func (s *Store) ClaimInvitationEmail(ctx context.Context, invitationID string, now time.Time) (bool, error) {
	tx, err := s.beginTx(ctx)
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
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/invitation-email/"+address); err != nil {
			return false, err
		}
	}
	at := now.UTC().UnixNano()
	since := now.Add(-24 * time.Hour).UnixNano()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM invitation_email_log WHERE address = ? AND sent_at <= ?`, address, since); err != nil {
		return false, err
	}
	var sent int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM invitation_email_log WHERE address = ? AND sent_at > ?`, address, since).Scan(&sent); err != nil {
		return false, err
	}
	if sent >= MaxInvitationEmailsPerDay {
		return false, nil
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE invitations SET emailed_at = ? WHERE id = ? AND emailed_at IS NULL`, at, invitationID)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
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
	sum := sha256.Sum256([]byte(NormalizeEmail(email)))
	return hex.EncodeToString(sum[:])
}

func accountByEmailTx(ctx context.Context, tx *storeTx, email string) (_ Account, err error) {
	rows, err := tx.QueryContext(ctx, accountSelect+` WHERE email = ? ORDER BY email_verified DESC, created_at`, NormalizeEmail(email))
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
