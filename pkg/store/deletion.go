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
	// RecheckAt is when a finished deletion sweeps once more for what a
	// credential of the team wrote after the purge; nil once it has.
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

// safety: the record outlives the team's rows, which is how a deletion that
// failed half-way resumes and how the requester reads its state, so it lives
// with the deployment rather than with the team it deletes.
const teamDeletionsTableSQLite = `CREATE TABLE IF NOT EXISTS team_deletions (
    slug         TEXT PRIMARY KEY,
    requested_by TEXT NOT NULL DEFAULT '',
    requested_at INTEGER NOT NULL,
    state        TEXT NOT NULL CHECK (state IN ('pending', 'done')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    finished_at  INTEGER,
    recheck_at   INTEGER,
    lease_holder TEXT NOT NULL DEFAULT '',
    lease_until  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_team_deletions_state ON team_deletions(state);
CREATE TABLE IF NOT EXISTS invitation_email_log (
    address TEXT NOT NULL,
    sent_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_invitation_email_log_address ON invitation_email_log(address, sent_at);`

var teamDeletionsTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(teamDeletionsTableSQLite)

// safety: nullable, because an invitation written before this column was
// never mailed by the controller.
var invitationEmailCols = map[string]string{"emailed_at": "INTEGER"}

// safety: the count lives on the account rather than being counted from
// teams, because a deleted team's row goes and the creation it cost must not.
var accountTeamsCreatedCols = map[string]string{"teams_created": "INTEGER NOT NULL DEFAULT 0"}

const backfillTeamsCreated = `UPDATE accounts SET teams_created = (SELECT COUNT(*) FROM teams WHERE created_by = accounts.id)`

func applyDeletionMigrationSQLite(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(teamDeletionsTableSQLite) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := ensureColumnsSQLite(ctx, tx, "invitations", invitationEmailCols); err != nil {
		return err
	}
	if err := ensureColumnsSQLite(ctx, tx, "accounts", accountTeamsCreatedCols); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, backfillTeamsCreated)
	return err
}

func applyDeletionMigrationPostgres(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(teamDeletionsTablePostgres) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := addColumnsTx(ctx, tx, "invitations", invitationEmailCols); err != nil {
		return err
	}
	if err := addColumnsTx(ctx, tx, "accounts", accountTeamsCreatedCols); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, backfillTeamsCreated)
	return err
}

// RequestDeletion queues t's team for deletion on behalf of an owner. The
// owner must belong to another team, because every signed-in session lands
// in a team and an account left with none would sign in to a team it cannot
// use; deleting the account removes a last team instead. See
// [Tenant.markForDeletionTx] for what the request does at once. It returns
// the revoked token prefixes.
func (t *Tenant) RequestDeletion(ctx context.Context, actorID string, now time.Time) (TeamDeletion, []string, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	defer rollbackOrLog(tx)
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return TeamDeletion{}, nil, err
	}
	role, err := t.roleTx(ctx, tx, actorID)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	if role != RoleOwner {
		return TeamDeletion{}, nil, ErrRoleAboveOwn
	}
	var others int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memberships WHERE account_id = ? AND team <> ?`,
		actorID, string(t.team)).Scan(&others); err != nil {
		return TeamDeletion{}, nil, err
	}
	if others == 0 {
		return TeamDeletion{}, nil, ErrOnlyTeam
	}
	revoked, err := t.markForDeletionTx(ctx, tx, actorID, now)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	del, err := teamDeletionTx(ctx, tx, t.team)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	return del, revoked, tx.Commit()
}

// RequestTeamDeletion queues a team for deletion on the operator's word,
// with no membership check, for a deletion request that arrived by mail.
func (o *Operator) RequestTeamDeletion(ctx context.Context, team Team, now time.Time) (TeamDeletion, []string, error) {
	t := &Tenant{s: o.s, team: NormalizeTeam(team)}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	defer rollbackOrLog(tx)
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
	return del, revoked, tx.Commit()
}

// markForDeletionTx records the deletion and closes every way into the team
// before any row is removed: members leave (their sessions move to a team
// they still hold, or end), every token is revoked, open invitations are
// withdrawn, queued runs are cancelled and running ones asked to stop.
// [Store.ForTeam] refuses the team from here on. The rows and stored
// objects go later, in [Operator.PurgeTeam], because they may not fit one
// transaction. Repeating a pending request changes nothing it has not
// already changed.
func (t *Tenant) markForDeletionTx(ctx context.Context, tx *storeTx, requestedBy string, now time.Time) ([]string, error) {
	if t.team == DefaultTeam {
		return nil, fmt.Errorf("%w: the default team holds every single-team install's rows", ErrInvalidInput)
	}
	at := now.UTC().Unix()
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
	revoked, err := t.revokeAllTokensTx(ctx, tx, at)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE invitations SET withdrawn_at = ?
		WHERE team = ? AND accepted_at IS NULL AND withdrawn_at IS NULL`, at, string(t.team)); err != nil {
		return nil, err
	}
	if err := t.cancelActiveRunsTx(ctx, tx, now); err != nil {
		return nil, err
	}
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

func (t *Tenant) revokeAllTokensTx(ctx context.Context, tx *storeTx, at int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT prefix FROM tokens WHERE team = ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		string(t.team), at)
	if err != nil {
		return nil, err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tokens SET revoked_at = ? WHERE team = ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		at, string(t.team), at); err != nil {
		return nil, err
	}
	return prefixes, nil
}

// safety: a queued trigger is finished here the way CancelPendingTrigger
// finishes one, so no claimant takes it; a claimed one gets the cancel request
// its runner already watches for.
func (t *Tenant) cancelActiveRunsTx(ctx context.Context, tx *storeTx, now time.Time) error {
	ns := now.UnixNano()
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = ?, finished_at = ?, error = ?
		WHERE team = ? AND status = ?
		  AND id IN (SELECT id FROM triggers WHERE team = ? AND status = ?)`,
		runStatusCancelled, ns, "cancelled: the team is being deleted",
		string(t.team), runStatusPending, string(t.team), triggerStatusPending); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE triggers SET status = ?, cancel_requested_at = COALESCE(cancel_requested_at, ?)
		WHERE team = ? AND status = ?`,
		triggerStatusDone, ns, string(t.team), triggerStatusPending); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE triggers SET cancel_requested_at = COALESCE(cancel_requested_at, ?)
		WHERE team = ? AND status = ?`,
		ns, string(t.team), triggerStatusClaimed)
	return err
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
		ts := time.Unix(recheck.Int64, 0).UTC()
		d.RecheckAt = &ts
	}
	d.Team = Team(team)
	d.RequestedAt = time.Unix(requested, 0).UTC()
	if finished.Valid {
		ts := time.Unix(finished.Int64, 0).UTC()
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
	at := now.UTC().Unix()
	res, err := o.s.exec(ctx, `
		UPDATE team_deletions SET lease_holder = ?, lease_until = ?
		WHERE slug = ?
		  AND (state = ? OR (state = ? AND recheck_at IS NOT NULL AND recheck_at <= ?))
		  AND (lease_until IS NULL OR lease_until < ? OR lease_holder = ?)`,
		holder, now.Add(lease).UTC().Unix(), string(NormalizeTeam(team)),
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
		string(team), state, holder, now.UTC().Unix()).Scan(&n); err != nil {
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
	for _, table := range append(slices.DeleteFunc(slices.Clone(tenantTables), func(t string) bool { return t == "runs" }), "runs") {
		if _, err := o.s.exec(ctx, `DELETE FROM `+table+` WHERE team = ?`, string(team)); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	_, err := o.s.exec(ctx, `DELETE FROM sparkwing_meta WHERE key LIKE ?`,
		metaKeyStorageChargedThroughPrefix+string(team)+"/%")
	return err
}

// TeamRunIDs lists the ids of every run a team holds, so the caller can
// remove what object storage keeps under each run before [Operator.PurgeTeam]
// removes the rows that name them.
func (o *Operator) TeamRunIDs(ctx context.Context, team Team) (_ []string, err error) {
	rows, err := o.s.query(ctx, `SELECT id FROM runs WHERE team = ? ORDER BY id`, string(NormalizeTeam(team)))
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
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	at := now.UTC().Unix()
	res, err := tx.ExecContext(ctx, `
		UPDATE team_deletions SET state = ?, finished_at = ?, recheck_at = ?, last_error = '',
		    lease_holder = '', lease_until = NULL
		WHERE slug = ? AND state = ? AND lease_holder = ?`,
		TeamDeletionDone, at, now.Add(recheckAfter).UTC().Unix(), string(team), TeamDeletionPending, holder)
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
	return tx.Commit()
}

// FinishTeamRecheck sweeps a finished deletion's team once more and closes
// the deletion. A credential minted for the team before its deletion, such
// as a cache grant, can still write under its slug until it expires, and
// the recheck removes what it wrote. holder must hold the lease. The slug
// is never registered again, so nothing the sweep finds belongs to anyone.
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
	rows, err := s.query(ctx, accountSelect+` WHERE email = ? ORDER BY email_verified DESC, created_at`, NormalizeEmail(email))
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
		a.EmailVerified, a.ActiveTeam, a.CreatedAt = verified == 1, Team(active), time.Unix(created, 0).UTC()
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
	tx, err := s.beginTx(ctx)
	if err != nil {
		return AccountDeletion{}, err
	}
	defer rollbackOrLog(tx)
	acct, err := accountTx(ctx, tx, accountID)
	if err != nil {
		return AccountDeletion{}, err
	}
	if err := lockOwnedTeamsTx(ctx, tx, s, accountID); err != nil {
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
	at := now.UTC().Unix()
	rows, err := tx.QueryContext(ctx, `
		SELECT prefix FROM tokens WHERE created_by = ? AND (revoked_at IS NULL OR revoked_at > ?)`, accountID, at)
	if err != nil {
		return AccountDeletion{}, err
	}
	minted, err := scanPrefixes(rows)
	if err != nil {
		return AccountDeletion{}, err
	}
	res.RevokedPrefixes = append(res.RevokedPrefixes, minted...)
	names, err := accountPrincipalNamesTx(ctx, tx, acct)
	if err != nil {
		return AccountDeletion{}, err
	}
	for _, name := range names {
		if err := relabelPrincipalTx(ctx, tx, name); err != nil {
			return AccountDeletion{}, fmt.Errorf("delete account: %w", err)
		}
	}
	label := DeletedUserLabel
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE tokens SET revoked_at = ? WHERE created_by = ? AND (revoked_at IS NULL OR revoked_at > ?)`, []any{at, accountID, at}},
		{`UPDATE tokens SET principal = ?, created_by = '' WHERE created_by = ?`, []any{label, accountID}},
		{`DELETE FROM memberships WHERE account_id = ?`, []any{accountID}},
		{`DELETE FROM sessions WHERE account_id = ?`, []any{accountID}},
		{`DELETE FROM identities WHERE account_id = ?`, []any{accountID}},
		{`DELETE FROM invitations WHERE email = ?`, []any{acct.Email}},
		{`UPDATE invitations SET invited_by = '' WHERE invited_by = ?`, []any{accountID}},
		{`UPDATE invitations SET accepted_by = '' WHERE accepted_by = ?`, []any{accountID}},
		{`UPDATE github_runner_bindings SET created_by = '' WHERE created_by = ?`, []any{accountID}},
		{`UPDATE teams SET created_by = '' WHERE created_by = ?`, []any{accountID}},
		{`UPDATE team_deletions SET requested_by = '' WHERE requested_by = ?`, []any{accountID}},
		{`DELETE FROM accounts WHERE id = ?`, []any{accountID}},
	} {
		if _, err := tx.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return AccountDeletion{}, fmt.Errorf("delete account: %w", err)
		}
	}
	return res, tx.Commit()
}

// ownedTeamsTx sorts the teams an account is the only owner of into those it
// is also the only member of, which go with the account, and those with other
// members, which block the deletion.
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
func lockOwnedTeamsTx(ctx context.Context, tx *storeTx, s *Store, accountID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT team FROM memberships WHERE account_id = ? AND role = ? ORDER BY team`, accountID, string(RoleOwner))
	if err != nil {
		return err
	}
	teams, err := scanPrefixes(rows)
	if err != nil {
		return err
	}
	for _, team := range teams {
		if err := (&Tenant{s: s, team: Team(team)}).lockTeamTx(ctx, tx); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// principalName is a name a row may carry for a deleted account. An address
// is the account's in every team; a token's principal is a label its team
// chose, so it names the account only inside that team.
type principalName struct {
	name string
	team string
}

// accountPrincipalNamesTx lists the names a row may carry for this account:
// its address now and every address an identity asserted for it, in every
// team, and the principal of every token it minted, in that token's team.
// Principal columns hold a name rather than an account id, so these are what
// the relabel matches.
func accountPrincipalNamesTx(ctx context.Context, tx *storeTx, acct Account) (_ []principalName, err error) {
	names := []principalName{{name: acct.Email}}
	add := func(n principalName) {
		if n.name != "" && n.name != DeletedUserLabel && !slices.Contains(names, n) &&
			!slices.Contains(names, principalName{name: n.name}) {
			names = append(names, n)
		}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT email, '' FROM identities WHERE account_id = ?
		UNION SELECT principal, team FROM tokens WHERE created_by = ?`, acct.ID, acct.ID)
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

// relabelPrincipalTx replaces a name with [DeletedUserLabel] in every column
// that records who acted, in every team for an address and in its own team
// for a token principal. Amounts on usage and billing rows stay; egress usage
// for an address merges into the label's row for the month.
func relabelPrincipalTx(ctx context.Context, tx *storeTx, n principalName) error {
	label := DeletedUserLabel
	scope, args := "", []any{label, n.name}
	if n.team != "" {
		scope, args = ` AND team = ?`, append(args, n.team)
	}
	for _, col := range []struct{ table, column string }{
		{"runs", "created_principal"},
		{"triggers", "trigger_user"},
		{"triggers", "claim_principal"},
		{"approvals", "approver"},
		{"cron_schedules", "armed_by"},
		{"node_bounces", "requested_by"},
		{"debug_pauses", "released_by"},
		{"credit_grants", "created_by"},
		{"nodes", "claim_principal"},
		{"node_claim_offers", "claim_principal"},
		{"secrets", "principal"},
	} {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+col.table+` SET `+col.column+` = ? WHERE `+col.column+` = ?`+scope, args...); err != nil {
			return fmt.Errorf("relabel %s.%s: %w", col.table, col.column, err)
		}
	}
	// safety: egress rows carry no team, so a token principal another team
	// may share is left alone rather than merged into the label.
	if n.team == "" {
		if _, err := tx.ExecContext(ctx, `
		INSERT INTO egress_usage (principal, month, bytes, updated_at)
		SELECT ?, month, bytes, updated_at FROM egress_usage WHERE principal = ?
		ON CONFLICT (principal, month) DO UPDATE SET
		    bytes = egress_usage.bytes + excluded.bytes, updated_at = excluded.updated_at`, label, n.name); err != nil {
			return fmt.Errorf("relabel egress usage: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM egress_usage WHERE principal = ?`, n.name); err != nil {
			return err
		}
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
	at := now.UTC().Unix()
	since := at - int64((24 * time.Hour).Seconds())
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
