package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SignUpMode is the stored half of the sign-up gate: whether a new account is
// admitted or placed on the waitlist. Accounts that already exist are never
// affected by it.
type SignUpMode string

// The two gate modes.
const (
	SignUpOpen     SignUpMode = "open"
	SignUpWaitlist SignUpMode = "waitlist"
)

// ParseSignUpMode reads a mode an operator typed.
func ParseSignUpMode(raw string) (SignUpMode, error) {
	switch m := SignUpMode(strings.ToLower(strings.TrimSpace(raw))); m {
	case SignUpOpen, SignUpWaitlist:
		return m, nil
	default:
		return "", fmt.Errorf("%w: sign-up mode %q is not open or waitlist", ErrInvalidInput, raw)
	}
}

// Why a new account landed on the waitlist. Each is a fixed word, so a metric
// label or an operator's filter can branch on it.
const (
	WaitlistReasonDeployment = "deployment"
	WaitlistReasonOperator   = "operator"
	WaitlistReasonFreeTier   = "free_tier_closed"
	WaitlistReasonHourly     = "hourly_signups"
	WaitlistReasonDaily      = "daily_signups"
	WaitlistReasonGitHubAge  = "github_account_age"
)

// WaitlistReasons lists every reason a new account can be waitlisted for.
func WaitlistReasons() []string {
	return []string{
		WaitlistReasonDeployment, WaitlistReasonOperator, WaitlistReasonFreeTier,
		WaitlistReasonHourly, WaitlistReasonDaily, WaitlistReasonGitHubAge,
	}
}

// Sign-up gate defaults. A bot farm of provider accounts costs one personal
// space each, so the limits bound how many free spaces an hour or a day can
// hand out before a human looks; a launch-day spike from real people lands on
// the waitlist rather than being refused, and an operator admits it in bulk.
const (
	DefaultSignUpHourlyLimit    = 50
	DefaultSignUpDailyLimit     = 500
	DefaultSignUpHourlyWarn     = 20
	DefaultGitHubMinAccountDays = 7
)

// SignUpLimits are the operator's thresholds. A zero limit turns that check
// off.
type SignUpLimits struct {
	// HourlyLimit is how many accounts the last hour may create before the
	// gate closes itself.
	HourlyLimit int
	// DailyLimit is the same bound over the last 24 hours.
	DailyLimit int
	// HourlyWarn is the lower hourly count past which the controller logs and
	// counts a velocity warning without closing anything.
	HourlyWarn int
	// GitHubMinAccountDays is the youngest GitHub account, in days, that a
	// sign-up admits; a younger one is waitlisted.
	GitHubMinAccountDays int
}

// DefaultSignUpLimits are the limits of a deployment nobody has tuned.
func DefaultSignUpLimits() SignUpLimits {
	return SignUpLimits{
		HourlyLimit: DefaultSignUpHourlyLimit, DailyLimit: DefaultSignUpDailyLimit,
		HourlyWarn: DefaultSignUpHourlyWarn, GitHubMinAccountDays: DefaultGitHubMinAccountDays,
	}
}

// SignUpGate is the stored gate with the provenance an incident asks for
// first: what set it, why, and when.
type SignUpGate struct {
	Mode      SignUpMode
	Source    string
	Reason    string
	SetBy     string
	UpdatedAt time.Time
	Limits    SignUpLimits
}

// SignUpConditions are the facts from outside the store that a new account's
// admission reads.
type SignUpConditions struct {
	// ForceWaitlist is the deployment's own setting, which no stored state
	// overrides.
	ForceWaitlist bool
	// FreeTierClosed reports that the free storage the deployment offers is
	// spent, so a new personal space would hold an allowance nobody can pay for.
	FreeTierClosed bool
}

// SignUpCounts are the accounts created over the two windows the gate reads,
// and the accounts waiting on the list now.
type SignUpCounts struct {
	LastHour   int
	LastDay    int
	Waitlisted int
}

// WaitlistedAccount is one account waiting for admission.
type WaitlistedAccount struct {
	Account
	WaitlistedAt time.Time
	Reason       string
}

// ErrWaitlisted refuses what a waitlisted account may not do yet.
var ErrWaitlisted = errors.New("store: this account is on the sign-up waitlist")

const signUpGateTableSQLite = `
CREATE TABLE IF NOT EXISTS signup_gate (
    id                      INTEGER PRIMARY KEY CHECK (id = 1),
    mode                    TEXT NOT NULL CHECK (mode IN ('open', 'waitlist')),
    source                  TEXT NOT NULL DEFAULT '',
    reason                  TEXT NOT NULL DEFAULT '',
    set_by                  TEXT NOT NULL DEFAULT '',
    updated_at              INTEGER NOT NULL,
    hourly_limit            INTEGER NOT NULL,
    daily_limit             INTEGER NOT NULL,
    hourly_warn             INTEGER NOT NULL,
    github_min_account_days INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_accounts_created ON accounts(created_at);
CREATE INDEX IF NOT EXISTS idx_accounts_waitlisted ON accounts(waitlisted_at) WHERE waitlisted_at <> 0;
`

var signUpGateTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(signUpGateTableSQLite)

// safety: zero is "never waitlisted", so every account that exists when the
// column arrives keeps its standing. The stamp is in nanoseconds because it
// orders the list, and a burst puts many sign-ups in one second.
var accountWaitlistCols = map[string]string{
	"waitlisted_at":   "INTEGER NOT NULL DEFAULT 0",
	"waitlist_reason": "TEXT NOT NULL DEFAULT ''",
}

func applySignUpGateMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if err := ensureColumnsSQLite(ctx, tx, "accounts", accountWaitlistCols); err != nil {
		return err
	}
	for _, stmt := range splitStatements(signUpGateTableSQLite) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func applySignUpGateMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "accounts", accountWaitlistCols); err != nil {
		return err
	}
	for _, stmt := range splitStatements(signUpGateTablePostgres) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

const signUpGateSelect = `SELECT mode, source, reason, set_by, updated_at,
	hourly_limit, daily_limit, hourly_warn, github_min_account_days FROM signup_gate WHERE id = 1`

// safety: a deployment with no row is open with the default limits, because
// nobody has had a reason to close it.
func scanSignUpGate(row rowScanner) (SignUpGate, error) {
	var g SignUpGate
	var mode string
	var updated int64
	err := row.Scan(&mode, &g.Source, &g.Reason, &g.SetBy, &updated,
		&g.Limits.HourlyLimit, &g.Limits.DailyLimit, &g.Limits.HourlyWarn, &g.Limits.GitHubMinAccountDays)
	if errors.Is(err, sql.ErrNoRows) {
		return SignUpGate{Mode: SignUpOpen, Source: "default", UpdatedAt: time.Unix(0, 0).UTC(), Limits: DefaultSignUpLimits()}, nil
	}
	if err != nil {
		return SignUpGate{}, err
	}
	g.Mode = SignUpMode(mode)
	g.UpdatedAt = time.Unix(updated, 0).UTC()
	return g, nil
}

// SignUpGate reads the stored gate.
func (s *Store) SignUpGate(ctx context.Context) (SignUpGate, error) {
	return scanSignUpGate(s.queryRow(ctx, signUpGateSelect))
}

func writeSignUpGateTx(ctx context.Context, tx *storeTx, g SignUpGate) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO signup_gate (id, mode, source, reason, set_by, updated_at,
			hourly_limit, daily_limit, hourly_warn, github_min_account_days)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET mode = excluded.mode, source = excluded.source,
			reason = excluded.reason, set_by = excluded.set_by, updated_at = excluded.updated_at,
			hourly_limit = excluded.hourly_limit, daily_limit = excluded.daily_limit,
			hourly_warn = excluded.hourly_warn, github_min_account_days = excluded.github_min_account_days`,
		string(g.Mode), g.Source, g.Reason, g.SetBy, g.UpdatedAt.UTC().Unix(),
		g.Limits.HourlyLimit, g.Limits.DailyLimit, g.Limits.HourlyWarn, g.Limits.GitHubMinAccountDays)
	return err
}

func (s *Store) updateSignUpGate(ctx context.Context, change func(*SignUpGate) error) (SignUpGate, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return SignUpGate{}, err
	}
	defer rollbackOrLog(tx)
	g, err := scanSignUpGate(tx.QueryRowContext(ctx, signUpGateSelect+tx.forUpdate()))
	if err != nil {
		return SignUpGate{}, err
	}
	if err := change(&g); err != nil {
		return SignUpGate{}, err
	}
	if err := writeSignUpGateTx(ctx, tx, g); err != nil {
		return SignUpGate{}, err
	}
	return g, tx.Commit()
}

// SetSignUpMode records the operator's mode. Opening the gate also restarts
// the velocity windows, so the burst that closed it does not close it again
// on the next sign-up.
func (s *Store) SetSignUpMode(ctx context.Context, mode SignUpMode, reason, setBy string, now time.Time) (SignUpGate, error) {
	if _, err := ParseSignUpMode(string(mode)); err != nil {
		return SignUpGate{}, err
	}
	return s.updateSignUpGate(ctx, func(g *SignUpGate) error {
		g.Mode, g.Source, g.Reason, g.SetBy, g.UpdatedAt = mode, WaitlistReasonOperator, strings.TrimSpace(reason), setBy, now
		return nil
	})
}

// SetSignUpLimits replaces the thresholds. It leaves the mode and its
// provenance alone.
func (s *Store) SetSignUpLimits(ctx context.Context, limits SignUpLimits) (SignUpGate, error) {
	if limits.HourlyLimit < 0 || limits.DailyLimit < 0 || limits.HourlyWarn < 0 || limits.GitHubMinAccountDays < 0 {
		return SignUpGate{}, fmt.Errorf("%w: sign-up limits are zero or more", ErrInvalidInput)
	}
	return s.updateSignUpGate(ctx, func(g *SignUpGate) error {
		g.Limits = limits
		return nil
	})
}

// SignUpCounts reads the accounts created in the last hour and the last day,
// and how many wait on the list.
func (s *Store) SignUpCounts(ctx context.Context, now time.Time) (SignUpCounts, error) {
	var c SignUpCounts
	at := now.UTC().Unix()
	err := s.queryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM accounts WHERE created_at > ?),
			(SELECT COUNT(*) FROM accounts WHERE created_at > ?),
			(SELECT COUNT(*) FROM accounts WHERE waitlisted_at <> 0)`,
		at-int64(time.Hour/time.Second), at-int64(24*time.Hour/time.Second)).
		Scan(&c.LastHour, &c.LastDay, &c.Waitlisted)
	return c, err
}

type signUpDecision struct {
	reason  string
	tripped *SignUpGate
}

// safety: counts start no earlier than the operator last set the mode, so a reopened gate does not trip
// on the burst it already dealt with. Only admitted accounts count: a waitlisted one holds no space, and
// counting it would let a farm of young GitHub accounts close the gate on everyone else. The gate row
// is locked on Postgres so two sign-ups that both cross a limit record one closure.
func decideSignUpTx(ctx context.Context, tx *storeTx, p SignInProfile, c SignUpConditions, now time.Time) (signUpDecision, error) {
	if c.ForceWaitlist {
		return signUpDecision{reason: WaitlistReasonDeployment}, nil
	}
	g, err := scanSignUpGate(tx.QueryRowContext(ctx, signUpGateSelect+tx.forUpdate()))
	if err != nil {
		return signUpDecision{}, err
	}
	if g.Mode == SignUpWaitlist {
		reason := g.Source
		if reason == "" {
			reason = WaitlistReasonOperator
		}
		return signUpDecision{reason: reason}, nil
	}
	if c.FreeTierClosed {
		return signUpDecision{reason: WaitlistReasonFreeTier}, nil
	}
	at := now.UTC().Unix()
	for _, w := range []struct {
		reason string
		limit  int
		span   time.Duration
		label  string
	}{
		{WaitlistReasonHourly, g.Limits.HourlyLimit, time.Hour, "hour"},
		{WaitlistReasonDaily, g.Limits.DailyLimit, 24 * time.Hour, "day"},
	} {
		if w.limit <= 0 {
			continue
		}
		since := at - int64(w.span/time.Second)
		if g.UpdatedAt.Unix() > since {
			since = g.UpdatedAt.Unix()
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM accounts WHERE created_at > ? AND waitlisted_at = 0`, since).Scan(&n); err != nil {
			return signUpDecision{}, err
		}
		if n < w.limit {
			continue
		}
		g.Mode, g.Source, g.SetBy, g.UpdatedAt = SignUpWaitlist, w.reason, "automatic", now
		g.Reason = fmt.Sprintf("%d admitted accounts in the last %s reached the limit of %d", n, w.label, w.limit)
		if err := writeSignUpGateTx(ctx, tx, g); err != nil {
			return signUpDecision{}, err
		}
		return signUpDecision{reason: w.reason, tripped: &g}, nil
	}
	if p.Provider == ProviderGitHub && g.Limits.GitHubMinAccountDays > 0 {
		minAge := time.Duration(g.Limits.GitHubMinAccountDays) * 24 * time.Hour
		// safety: an account whose age GitHub did not state cannot show it is
		// old enough, so it waits rather than slipping through.
		if p.ProviderAccountCreatedAt.IsZero() || now.Sub(p.ProviderAccountCreatedAt) < minAge {
			return signUpDecision{reason: WaitlistReasonGitHubAge}, nil
		}
	}
	return signUpDecision{}, nil
}

// WaitlistedAccounts lists the accounts waiting for admission, oldest first.
// A limit of zero or less reads them all.
func (s *Store) WaitlistedAccounts(ctx context.Context, limit int) (_ []WaitlistedAccount, err error) {
	q := `SELECT id, email, email_verified, name, active_team, created_at, waitlisted_at, waitlist_reason
		FROM accounts WHERE waitlisted_at <> 0 ORDER BY waitlisted_at, id`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	out := []WaitlistedAccount{}
	for rows.Next() {
		var w WaitlistedAccount
		var verified int
		var active string
		var created, waitlisted int64
		if err := rows.Scan(&w.ID, &w.Email, &verified, &w.Name, &active, &created, &waitlisted, &w.Reason); err != nil {
			return nil, err
		}
		w.EmailVerified, w.ActiveTeam = verified == 1, Team(active)
		w.CreatedAt, w.WaitlistedAt = time.Unix(created, 0).UTC(), time.Unix(0, waitlisted).UTC()
		w.Waitlisted = true
		out = append(out, w)
	}
	return out, rows.Err()
}

// ApproveWaitlisted admits the named accounts: each leaves the waitlist and,
// if it belongs to no team, gets its personal space. An id that is not on the
// waitlist is skipped, so approving twice admits once. It returns the accounts
// it admitted.
func (s *Store) ApproveWaitlisted(ctx context.Context, ids []string, now time.Time) ([]Account, error) {
	out := []Account{}
	for _, id := range ids {
		acct, ok, err := s.approveOne(ctx, id, now)
		if err != nil {
			return out, err
		}
		if ok {
			out = append(out, acct)
		}
	}
	return out, nil
}

// ApproveOldestWaitlisted admits the n accounts that have waited longest.
func (s *Store) ApproveOldestWaitlisted(ctx context.Context, n int, now time.Time) ([]Account, error) {
	if n <= 0 {
		return nil, fmt.Errorf("%w: approve at least one account", ErrInvalidInput)
	}
	waiting, err := s.WaitlistedAccounts(ctx, n)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(waiting))
	for _, w := range waiting {
		ids = append(ids, w.ID)
	}
	return s.ApproveWaitlisted(ctx, ids, now)
}

// safety: a personal slug can lose a race to a concurrent sign-in, and the
// retry re-reads what the winner took, as a first sign-in does.
func (s *Store) approveOne(ctx context.Context, id string, now time.Time) (Account, bool, error) {
	var last error
	for range 5 {
		acct, ok, err := s.approveOneOnce(ctx, id, now)
		if err == nil || (!isUniqueViolation(err) && !errors.Is(err, ErrSlugTaken)) {
			return acct, ok, err
		}
		last = err
	}
	return Account{}, false, last
}

func (s *Store) approveOneOnce(ctx context.Context, id string, now time.Time) (Account, bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Account{}, false, err
	}
	defer rollbackOrLog(tx)
	at := now.UTC().Unix()
	res, err := tx.ExecContext(ctx,
		`UPDATE accounts SET waitlisted_at = 0, waitlist_reason = '', updated_at = ? WHERE id = ? AND waitlisted_at <> 0`,
		at, id)
	if err != nil {
		return Account{}, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return Account{}, false, err
	}
	acct, err := accountTx(ctx, tx, id)
	if err != nil {
		return Account{}, false, err
	}
	var members int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memberships WHERE account_id = ?`, id).Scan(&members); err != nil {
		return Account{}, false, err
	}
	if members == 0 {
		given, _, _ := strings.Cut(strings.TrimSpace(acct.Name), " ")
		display := personalDisplayName(SignInProfile{Email: acct.Email, GivenName: given})
		if _, err := createPersonalTeamTx(ctx, tx, id, slugBase(acct.Email), display, now); err != nil {
			return Account{}, false, err
		}
	}
	if err := settleActiveTeamTx(ctx, tx, id, at); err != nil {
		return Account{}, false, err
	}
	if acct, err = accountTx(ctx, tx, id); err != nil {
		return Account{}, false, err
	}
	return acct, true, tx.Commit()
}
