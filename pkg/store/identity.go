package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Role is an account's authority inside one team. The set is closed: there
// are no custom roles.
type Role string

// The three roles, most authority first.
const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleReader Role = "reader"
)

// Valid reports whether r is one of the three roles. The schema carries the
// same constraint, so a bug here cannot store a role that authorization would
// later read as unknown.
func (r Role) Valid() bool { return r.rank() > 0 }

// safety: the most recent provider assertion owns an address, so another account's verified claim on
// it is withdrawn before this one takes it.
func claimEmailTx(ctx context.Context, tx *storeTx, accountID, email string, at int64) error {
	if err := releaseEmailTx(ctx, tx, email, accountID, at); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE accounts SET email = ?, email_verified = 1, updated_at = ? WHERE id = ?`, email, at, accountID)
	return err
}

func releaseEmailTx(ctx context.Context, tx *storeTx, email, keepID string, at int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts SET email_verified = 0, updated_at = ?
		WHERE email = ? AND email_verified = 1 AND id <> ?`, at, email, keepID)
	return err
}

// safety: an unknown role ranks below every real one, so a row holding one grants nothing.
func (r Role) rank() int {
	switch r {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleReader:
		return 1
	default:
		return 0
	}
}

// AtLeast reports whether r carries at least as much authority as other.
func (r Role) AtLeast(other Role) bool { return r.rank() > 0 && r.rank() >= other.rank() }

// Identity providers an identity row can name.
const (
	ProviderGoogle = "google"
	ProviderGitHub = "github"
)

// safety: an invitation that never expires is a standing grant to whoever ends up holding the address.
const invitationTTL = 7 * 24 * time.Hour

// safety: a slug becomes a hostname label, so it is held to what a DNS label allows.
const (
	minSlugLen        = 3
	maxSlugLen        = 30
	maxDisplayNameLen = 80
)

// safety: each would take a hostname the platform answers on, and default is every single-team install's key.
var reservedSlugs = map[string]bool{
	"www": true, "api": true, "app": true, "console": true, "docs": true, "status": true,
	"admin": true, "mail": true, "smtp": true, "ns1": true, "login": true, "auth": true,
	"demo": true, "sparkwing": true, "cache": true, "logs": true,
	string(DefaultTeam): true,
}

// safety: demo-* names the hosts the operator stands up for demonstrations.
const reservedSlugPrefix = "demo-"

func isReservedSlug(slug string) bool {
	return reservedSlugs[slug] || strings.HasPrefix(slug, reservedSlugPrefix)
}

// Team-wide ceilings. Every runner token multiplies each per-principal budget, and every invitation
// may become a mail the deployment sends, so a team holds a bounded number of both.
const (
	MaxRunnerTokensPerTeam = 10
	MaxInvitationsPerDay   = 100
	MaxOpenInvitations     = 50
)

// MaxCreatedTeams bounds how many teams one user can create, their personal
// space included, because each team is a tenant the deployment pays to hold.
const MaxCreatedTeams = 10

// Identity errors. Callers map these onto status codes.
var (
	ErrInvalidSlug      = errors.New("store: invalid team slug")
	ErrInvalidInput     = errors.New("store: invalid input")
	ErrSlugTaken        = errors.New("store: that team slug is taken")
	ErrLastOwner        = errors.New("store: a team keeps at least one owner")
	ErrRoleAboveOwn     = errors.New("store: cannot grant or change a role above your own")
	ErrNotMember        = errors.New("store: not a member of this team")
	ErrAlreadyMember    = errors.New("store: already a member of this team")
	ErrInvitationClosed = errors.New("store: invitation is expired or already used")
	ErrEmailMismatch    = errors.New("store: invitation is addressed to another email")
	ErrInvitationOpen   = errors.New("store: that address already has an open invitation")
	ErrUnverifiedEmail  = errors.New("store: a sign-in needs a verified email")
	ErrTeamLimit        = errors.New("store: this user has created as many teams as one user may")
	ErrRunnerTokenLimit = errors.New("store: this team holds as many runner tokens as a team may")
	ErrInvitationLimit  = errors.New("store: this team has sent as many invitations as it may for now")
)

// Account is one human, what the API calls a user. Email is the address the
// identity provider verified. ActiveTeam is the team the human last worked
// in, which the next sign-in returns them to.
type Account struct {
	ID            string
	Email         string
	EmailVerified bool
	Name          string
	ActiveTeam    Team
	CreatedAt     time.Time
}

// TeamInfo is a team's registry row.
type TeamInfo struct {
	Slug        Team
	DisplayName string
	CreatedBy   string
}

// Label is the name to show for a team: its display name, or its slug when
// it has none.
func (t TeamInfo) Label() string {
	if t.DisplayName != "" {
		return t.DisplayName
	}
	return string(t.Slug)
}

// Membership is one account in one team.
type Membership struct {
	Team        Team
	DisplayName string
	AccountID   string
	Email       string
	Name        string
	Role        Role
}

// Invitation offers one address one role in one team.
type Invitation struct {
	ID              string
	Team            Team
	TeamDisplayName string
	Email           string
	Role            Role
	InvitedBy       string
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

// SignInProfile is what an identity provider asserted about the person who
// just authenticated.
type SignInProfile struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	GivenName     string
}

// SignInResult reports what a sign-in did.
type SignInResult struct {
	Account Account
	// NewAccount is true when the sign-in created the human.
	NewAccount bool
	// Linked is true when an existing human gained this identity.
	Linked bool
	// PersonalTeam names the team the sign-in created, if it created one.
	PersonalTeam Team
}

const identityTablesSQLite = `
CREATE TABLE IF NOT EXISTS accounts (
    id             TEXT PRIMARY KEY,
    email          TEXT NOT NULL,
    email_verified INTEGER NOT NULL DEFAULT 0,
    name           TEXT NOT NULL DEFAULT '',
    active_team    TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_verified_email ON accounts(email) WHERE email_verified = 1;
CREATE TABLE IF NOT EXISTS identities (
    provider       TEXT NOT NULL,
    subject        TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    email          TEXT NOT NULL DEFAULT '',
    email_verified INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    PRIMARY KEY (provider, subject)
);
CREATE INDEX IF NOT EXISTS idx_identities_account ON identities(account_id);
CREATE TABLE IF NOT EXISTS memberships (
    team       TEXT NOT NULL,
    account_id TEXT NOT NULL,
    role       TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'reader')),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (team, account_id)
);
CREATE INDEX IF NOT EXISTS idx_memberships_account ON memberships(account_id);
CREATE TABLE IF NOT EXISTS invitations (
    id          TEXT PRIMARY KEY,
    team        TEXT NOT NULL,
    email       TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'reader')),
    invited_by  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    accepted_at INTEGER,
    accepted_by TEXT NOT NULL DEFAULT '',
    withdrawn_at INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_invitations_open ON invitations(team, email)
    WHERE accepted_at IS NULL AND withdrawn_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_invitations_email ON invitations(email);
`

var identityTablesPostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(identityTablesSQLite)

var (
	teamIdentityCols = map[string]string{
		"display_name": "TEXT NOT NULL DEFAULT ''",
		"created_by":   "TEXT NOT NULL DEFAULT ''",
	}
	sessionAccountCols = map[string]string{"account_id": "TEXT NOT NULL DEFAULT ''"}
	tokenCreatorCols   = map[string]string{"created_by": "TEXT NOT NULL DEFAULT ''"}
)

func applyIdentityMigrationSQLite(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(identityTablesSQLite) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for table, cols := range map[string]map[string]string{
		"teams": teamIdentityCols, "sessions": sessionAccountCols, "tokens": tokenCreatorCols,
	} {
		if err := ensureColumnsSQLite(ctx, tx, table, cols); err != nil {
			return err
		}
	}
	return nil
}

func applyIdentityMigrationPostgres(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(identityTablesPostgres) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for table, cols := range map[string]map[string]string{
		"teams": teamIdentityCols, "sessions": sessionAccountCols, "tokens": tokenCreatorCols,
	} {
		if err := addColumnsTx(ctx, tx, table, cols); err != nil {
			return err
		}
	}
	return nil
}

func splitStatements(script string) []string {
	var out []string
	for _, stmt := range strings.Split(script, ";") {
		if s := strings.TrimSpace(stmt); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func newIdentityID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// NormalizeEmail lowercases and trims an address so one human's address
// compares equal however it was typed. The local part is otherwise left alone:
// stripping dots or plus tags is one provider's rule, and applying it to every
// domain would merge addresses that belong to different people.
func NormalizeEmail(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// LooksLikeEmail rejects obvious nonsense before a row is written. Only
// sending to an address proves it receives mail.
func LooksLikeEmail(email string) bool {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 || strings.ContainsAny(email, " \t\r\n,") {
		return false
	}
	return strings.Contains(email[at+1:], ".")
}

// ValidateSlug checks a proposed team slug against what a hostname label
// allows and against the names the platform keeps for itself.
func ValidateSlug(slug string) error {
	if len(slug) < minSlugLen || len(slug) > maxSlugLen {
		return fmt.Errorf("%w: a slug is %d to %d characters", ErrInvalidSlug, minSlugLen, maxSlugLen)
	}
	if slug[0] == '-' || slug[len(slug)-1] == '-' {
		return fmt.Errorf("%w: a slug does not start or end with a hyphen", ErrInvalidSlug)
	}
	for _, r := range slug {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return fmt.Errorf("%w: a slug holds only lowercase letters, digits and hyphens", ErrInvalidSlug)
		}
	}
	if isReservedSlug(slug) {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidSlug, slug)
	}
	return nil
}

func slugBase(email string) string {
	local, _, _ := strings.Cut(email, "@")
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(local) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if !hyphen && b.Len() > 0 {
			b.WriteByte('-')
			hyphen = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if len(base) > maxSlugLen {
		base = strings.TrimRight(base[:maxSlugLen], "-")
	}
	if len(base) < minSlugLen || isReservedSlug(base) {
		base = strings.TrimRight(truncate(base, maxSlugLen-len("-space")), "-")
		if base == "" {
			base = "personal"
		}
		base += "-space"
		if isReservedSlug(base) {
			base = strings.TrimRight(truncate("space-"+base, maxSlugLen), "-")
		}
	}
	return base
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// safety: the suffix is the smallest free count from 2, never random noise, so the name still reads as
// the person's. Each candidate is claimed by an insert that skips a taken name, so concurrent sign-ins
// sharing a local part each land on their own slug instead of failing.
func createPersonalTeamTx(ctx context.Context, tx *storeTx, accountID, base, displayName string, now time.Time) (Team, error) {
	for n := 1; n < 1000; n++ {
		candidate := base
		if n > 1 {
			suffix := "-" + strconv.Itoa(n)
			candidate = strings.TrimRight(truncate(base, maxSlugLen-len(suffix)), "-") + suffix
		}
		err := createTeamTx(ctx, tx, accountID, Team(candidate), displayName, now)
		if err == nil {
			return Team(candidate), nil
		}
		if !errors.Is(err, ErrSlugTaken) {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: no free slug near %q", ErrSlugTaken, base)
}

func personalDisplayName(p SignInProfile) string {
	name := strings.TrimSpace(p.GivenName)
	if name == "" {
		name, _, _ = strings.Cut(p.Email, "@")
	}
	return truncate(name+"'s space", maxDisplayNameLen)
}

// ResolveSignIn turns an authenticated provider profile into an account and
// makes sure the account has a team to land in.
//
// The rule, in full:
//
//  1. An identity seen before belongs to the account it already belongs to.
//  2. Otherwise it attaches to an existing account only when the provider
//     asserts the address verified AND that address is verified on the
//     account. Both sides verified, or no link.
//  3. Otherwise it becomes a new account.
//
// Rule 2 is the security property. Linking on an address the provider has not
// verified lets anyone who types a victim's address into a new provider
// account walk into the victim's teams. This store refuses an unverified
// address outright, because no route here can later prove one.
//
// An account left holding no membership, new or returning, gets a personal
// space: a team whose only member is its owner.
func (s *Store) ResolveSignIn(ctx context.Context, p SignInProfile, now time.Time) (SignInResult, error) {
	p.Email = NormalizeEmail(p.Email)
	switch {
	case p.Provider == "" || p.Subject == "":
		return SignInResult{}, errors.New("store: sign-in names no provider or subject")
	case !p.EmailVerified || !LooksLikeEmail(p.Email):
		return SignInResult{}, ErrUnverifiedEmail
	}
	// safety: concurrent first sign-ins race on the verified-email index and on a personal slug; the
	// loser rolls back and its retry re-reads what the winner wrote, rather than failing a person
	// who did nothing wrong.
	var last error
	for range 5 {
		res, err := s.resolveSignInOnce(ctx, p, now)
		if err == nil || (!isUniqueViolation(err) && !errors.Is(err, ErrSlugTaken)) {
			return res, err
		}
		last = err
	}
	return SignInResult{}, last
}

func (s *Store) resolveSignInOnce(ctx context.Context, p SignInProfile, now time.Time) (SignInResult, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return SignInResult{}, err
	}
	defer rollbackOrLog(tx)
	at := now.UTC().Unix()

	var res SignInResult
	var accountID string
	err = tx.QueryRowContext(ctx,
		`SELECT account_id FROM identities WHERE provider = ? AND subject = ?`,
		p.Provider, p.Subject).Scan(&accountID)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE identities SET email = ?, email_verified = ? WHERE provider = ? AND subject = ?`,
			p.Email, boolInt(p.EmailVerified), p.Provider, p.Subject); err != nil {
			return SignInResult{}, fmt.Errorf("identity: refresh: %w", err)
		}
		// safety: the account's address is its linking and invitation key, so it follows what the
		// provider asserts now; an address left behind would let whoever holds it next walk in.
		if err := claimEmailTx(ctx, tx, accountID, p.Email, at); err != nil {
			return SignInResult{}, err
		}
	case errors.Is(err, sql.ErrNoRows):
		// safety: rule 2, the account side must hold this address verified too.
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM accounts WHERE email = ? AND email_verified = 1`, p.Email).Scan(&accountID)
		if err == nil {
			var sameProvider int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM identities WHERE account_id = ? AND provider = ?`,
				accountID, p.Provider).Scan(&sameProvider); err != nil {
				return SignInResult{}, err
			}
			// safety: one provider account per human, so a second subject from the same provider on this
			// address is a recycled address or another person; it gets a fresh account, never this one.
			if sameProvider > 0 {
				if err := releaseEmailTx(ctx, tx, p.Email, "", at); err != nil {
					return SignInResult{}, err
				}
				accountID, err = "", sql.ErrNoRows
			}
		}
		switch {
		case err == nil:
			res.Linked = true
		case errors.Is(err, sql.ErrNoRows):
			if accountID, err = newIdentityID(); err != nil {
				return SignInResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO accounts (id, email, email_verified, name, active_team, created_at, updated_at)
				VALUES (?, ?, ?, ?, '', ?, ?)`,
				accountID, p.Email, boolInt(p.EmailVerified), strings.TrimSpace(p.Name), at, at); err != nil {
				return SignInResult{}, fmt.Errorf("identity: create account: %w", err)
			}
			res.NewAccount = true
		default:
			return SignInResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO identities (provider, subject, account_id, email, email_verified, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			p.Provider, p.Subject, accountID, p.Email, boolInt(p.EmailVerified), at); err != nil {
			return SignInResult{}, fmt.Errorf("identity: record identity: %w", err)
		}
	default:
		return SignInResult{}, err
	}

	var members int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memberships WHERE account_id = ?`, accountID).Scan(&members); err != nil {
		return SignInResult{}, err
	}
	if members == 0 {
		slug, err := createPersonalTeamTx(ctx, tx, accountID, slugBase(p.Email), personalDisplayName(p), now)
		if err != nil {
			return SignInResult{}, err
		}
		res.PersonalTeam = slug
	}
	if err := settleActiveTeamTx(ctx, tx, accountID, at); err != nil {
		return SignInResult{}, err
	}
	if res.Account, err = accountTx(ctx, tx, accountID); err != nil {
		return SignInResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SignInResult{}, err
	}
	return res, nil
}

func settleActiveTeamTx(ctx context.Context, tx *storeTx, accountID string, at int64) error {
	var active string
	if err := tx.QueryRowContext(ctx,
		`SELECT active_team FROM accounts WHERE id = ?`, accountID).Scan(&active); err != nil {
		return err
	}
	if active != "" {
		var role string
		err := tx.QueryRowContext(ctx,
			`SELECT role FROM memberships WHERE team = ? AND account_id = ?`, active, accountID).Scan(&role)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var first string
	err := tx.QueryRowContext(ctx, `
		SELECT team FROM memberships WHERE account_id = ?
		ORDER BY created_at, team LIMIT 1`, accountID).Scan(&first)
	if errors.Is(err, sql.ErrNoRows) {
		first = ""
	} else if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE accounts SET active_team = ?, updated_at = ? WHERE id = ?`, first, at, accountID)
	return err
}

func accountTx(ctx context.Context, tx *storeTx, id string) (Account, error) {
	return scanAccount(tx.QueryRowContext(ctx, accountSelect+` WHERE id = ?`, id))
}

const accountSelect = `SELECT id, email, email_verified, name, active_team, created_at FROM accounts`

func scanAccount(row *sql.Row) (Account, error) {
	var a Account
	var verified int
	var active string
	var created int64
	if err := row.Scan(&a.ID, &a.Email, &verified, &a.Name, &active, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, ErrNotFound
		}
		return Account{}, err
	}
	a.EmailVerified = verified == 1
	a.ActiveTeam = Team(active)
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, nil
}

// Account reads one account.
func (s *Store) Account(ctx context.Context, id string) (Account, error) {
	return scanAccount(s.queryRow(ctx, accountSelect+` WHERE id = ?`, id))
}

// CreateTeam registers a team with its creator as owner and makes it the
// creator's active team. The registry row, the membership and the switch are
// one transaction, because a team with no owner is a team nobody can
// administer.
func (s *Store) CreateTeam(ctx context.Context, accountID string, slug Team, displayName string, now time.Time) (TeamInfo, error) {
	slug = NormalizeTeam(slug)
	if err := ValidateSlug(string(slug)); err != nil {
		return TeamInfo{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = string(slug)
	}
	if len(displayName) > maxDisplayNameLen {
		return TeamInfo{}, fmt.Errorf("%w: a display name is at most %d characters", ErrInvalidInput, maxDisplayNameLen)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return TeamInfo{}, err
	}
	defer rollbackOrLog(tx)
	var created int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams WHERE created_by = ?`, accountID).Scan(&created); err != nil {
		return TeamInfo{}, err
	}
	if created >= MaxCreatedTeams {
		return TeamInfo{}, ErrTeamLimit
	}
	if err := createTeamTx(ctx, tx, accountID, slug, displayName, now); err != nil {
		return TeamInfo{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET active_team = ?, updated_at = ? WHERE id = ?`,
		string(slug), now.UTC().Unix(), accountID); err != nil {
		return TeamInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamInfo{}, err
	}
	return TeamInfo{Slug: slug, DisplayName: displayName, CreatedBy: accountID}, nil
}

func createTeamTx(ctx context.Context, tx *storeTx, accountID string, slug Team, displayName string, now time.Time) error {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO teams (name, display_name, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO NOTHING`,
		string(slug), displayName, accountID, now.UnixNano(), now.UnixNano())
	if err != nil {
		return fmt.Errorf("identity: create team: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrSlugTaken
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memberships (team, account_id, role, created_at) VALUES (?, ?, ?, ?)`,
		string(slug), accountID, string(RoleOwner), now.UTC().Unix()); err != nil {
		return fmt.Errorf("identity: add owner: %w", err)
	}
	return nil
}

// TeamInfo reads a team's registry row.
func (s *Store) TeamInfo(ctx context.Context, team Team) (TeamInfo, error) {
	info := TeamInfo{Slug: NormalizeTeam(team)}
	err := s.queryRow(ctx, `SELECT display_name, created_by FROM teams WHERE name = ?`, string(info.Slug)).
		Scan(&info.DisplayName, &info.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamInfo{}, ErrNotFound
	}
	return info, err
}

// AccountMemberships lists the teams an account belongs to.
func (s *Store) AccountMemberships(ctx context.Context, accountID string) (_ []Membership, err error) {
	rows, err := s.query(ctx, `
		SELECT m.team, t.display_name, m.role FROM memberships m
		JOIN teams t ON t.name = m.team
		WHERE m.account_id = ? ORDER BY m.created_at, m.team`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	var out []Membership
	for rows.Next() {
		m := Membership{AccountID: accountID}
		var team, role string
		if err := rows.Scan(&team, &m.DisplayName, &role); err != nil {
			return nil, err
		}
		m.Team, m.Role = Team(team), Role(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// OpenInvitationsForEmail lists the unaccepted, unexpired invitations to an
// address, from every team.
func (s *Store) OpenInvitationsForEmail(ctx context.Context, email string, now time.Time) (_ []Invitation, err error) {
	rows, err := s.query(ctx, invitationSelect+`
		WHERE i.email = ? AND i.accepted_at IS NULL AND i.withdrawn_at IS NULL AND i.expires_at > ?
		ORDER BY i.created_at`, NormalizeEmail(email), now.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return scanInvitations(rows)
}

const invitationSelect = `
	SELECT i.id, i.team, t.display_name, i.email, i.role, i.invited_by, i.created_at, i.expires_at
	FROM invitations i JOIN teams t ON t.name = i.team`

func scanInvitations(rows *sql.Rows) ([]Invitation, error) {
	var out []Invitation
	for rows.Next() {
		var inv Invitation
		var team, role string
		var created, expires int64
		if err := rows.Scan(&inv.ID, &team, &inv.TeamDisplayName, &inv.Email, &role,
			&inv.InvitedBy, &created, &expires); err != nil {
			return nil, err
		}
		inv.Team, inv.Role = Team(team), Role(role)
		inv.CreatedAt, inv.ExpiresAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
		out = append(out, inv)
	}
	return out, rows.Err()
}

// AcceptInvitation turns an invitation into a membership and makes its team
// the account's active one. The account's verified email must be the address
// the invitation names: the invitation id is a bearer secret anyone it was
// forwarded to holds, and the verified email is what proves the person
// accepting is the person invited.
func (s *Store) AcceptInvitation(ctx context.Context, accountID, invitationID string, now time.Time) (Team, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackOrLog(tx)
	at := now.UTC().Unix()

	var team, email, role string
	var expires int64
	var accepted, withdrawn sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT team, email, role, expires_at, accepted_at, withdrawn_at FROM invitations WHERE id = ?`, invitationID).
		Scan(&team, &email, &role, &expires, &accepted, &withdrawn)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	acct, err := accountTx(ctx, tx, accountID)
	if err != nil {
		return "", err
	}
	// safety: the address check runs before the expiry and use checks, so a
	// holder of someone else's invitation learns nothing about its state.
	if !acct.EmailVerified || acct.Email != email {
		return "", ErrEmailMismatch
	}
	if accepted.Valid || withdrawn.Valid || expires <= at {
		return "", ErrInvitationClosed
	}
	// safety: the update repeats the unaccepted predicate and counts what it
	// changed, because two callers that both read it open would otherwise both
	// take it.
	res, err := tx.ExecContext(ctx, `
		UPDATE invitations SET accepted_at = ?, accepted_by = ?
		WHERE id = ? AND team = ? AND accepted_at IS NULL AND withdrawn_at IS NULL`, at, accountID, invitationID, team)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return "", ErrInvitationClosed
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memberships (team, account_id, role, created_at) VALUES (?, ?, ?, ?)`,
		team, accountID, role, at); err != nil {
		if isUniqueViolation(err) {
			return "", ErrAlreadyMember
		}
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE accounts SET active_team = ?, updated_at = ? WHERE id = ?`, team, at, accountID); err != nil {
		return "", err
	}
	return Team(team), tx.Commit()
}

// SetActiveTeam records team as the account's sticky team. The account must be
// a member of it.
func (s *Store) SetActiveTeam(ctx context.Context, accountID string, team Team, now time.Time) error {
	t := &Tenant{s: s, team: NormalizeTeam(team)}
	if _, err := t.MemberRole(ctx, accountID); err != nil {
		return err
	}
	_, err := s.exec(ctx, `UPDATE accounts SET active_team = ?, updated_at = ? WHERE id = ?`,
		string(t.team), now.UTC().Unix(), accountID)
	return err
}

// CreateAccountSession opens a browser session for an account in team. The
// session stores no scopes: they come from the account's membership on every
// request, so a demoted account loses them on its next request rather than
// at session expiry.
func (s *Store) CreateAccountSession(
	ctx context.Context, acct Account, team Team, ttl time.Duration, now time.Time,
) (rawSession, csrfToken string, sess *Session, err error) {
	if acct.ID == "" || team == "" {
		return "", "", nil, errors.New("sessions: account and team required")
	}
	if ttl <= 0 {
		return "", "", nil, errors.New("sessions: ttl must be positive")
	}
	rawSession, err = newSessionID()
	if err != nil {
		return "", "", nil, err
	}
	//nolint:contextcheck // the CSRF key is read or minted once per store under its own transaction, as CreateSession does
	if csrfToken, err = s.deriveCSRFToken(rawSession); err != nil {
		return "", "", nil, err
	}
	expires := now.Add(ttl).UTC()
	if _, err := s.exec(ctx, `
		INSERT INTO sessions (hash, team, principal, scopes, account_id, created_at, expires_at)
		VALUES (?, ?, ?, '', ?, ?, ?)`,
		sessionDigest(rawSession), string(team), acct.Email, acct.ID, now.UTC().Unix(), expires.Unix()); err != nil {
		return "", "", nil, fmt.Errorf("sessions: insert: %w", err)
	}
	return rawSession, csrfToken, &Session{
		ID: rawSession, Principal: acct.Email, Team: team, AccountID: acct.ID,
		CSRFToken: csrfToken, CreatedAt: now.UTC(), ExpiresAt: expires,
	}, nil
}

// SwitchSessionTeam moves an account's session from one team to another. It
// names the team the session is leaving, so a session already moved by a
// concurrent switch is reported rather than moved twice.
func (s *Store) SwitchSessionTeam(ctx context.Context, rawSession, accountID string, from, to Team) error {
	res, err := s.exec(ctx,
		`UPDATE sessions SET team = ? WHERE hash = ? AND account_id = ? AND team = ?`,
		string(to), sessionDigest(rawSession), accountID, string(from))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	return nil
}

// MemberRole reads an account's role in t's team, or ErrNotMember.
func (t *Tenant) MemberRole(ctx context.Context, accountID string) (Role, error) {
	var role string
	err := t.s.queryRow(ctx,
		`SELECT role FROM memberships WHERE team = ? AND account_id = ?`, string(t.team), accountID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotMember
	}
	return Role(role), err
}

// Info reads t's team registry row.
func (t *Tenant) Info(ctx context.Context) (TeamInfo, error) {
	return t.s.TeamInfo(ctx, t.team)
}

// Rename changes t's display name. The slug is the tenant key and stays.
func (t *Tenant) Rename(ctx context.Context, displayName string, now time.Time) (TeamInfo, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > maxDisplayNameLen {
		return TeamInfo{}, fmt.Errorf("%w: a display name is 1 to %d characters", ErrInvalidInput, maxDisplayNameLen)
	}
	if _, err := t.s.exec(ctx, `UPDATE teams SET display_name = ?, updated_at = ? WHERE name = ?`,
		displayName, now.UnixNano(), string(t.team)); err != nil {
		return TeamInfo{}, err
	}
	return t.Info(ctx)
}

// Members lists t's members.
func (t *Tenant) Members(ctx context.Context) (_ []Membership, err error) {
	rows, err := t.s.query(ctx, `
		SELECT m.account_id, a.email, a.name, m.role FROM memberships m
		JOIN accounts a ON a.id = m.account_id
		WHERE m.team = ? ORDER BY m.created_at, a.email`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	var out []Membership
	for rows.Next() {
		m := Membership{Team: t.team}
		var role string
		if err := rows.Scan(&m.AccountID, &m.Email, &m.Name, &role); err != nil {
			return nil, err
		}
		m.Role = Role(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMemberRole changes subject's role. The actor may not grant a role above
// their own, may not re-role someone who outranks them, and may not demote
// the last owner, because a team without an owner cannot be administered and
// nobody can put one back.
func (t *Tenant) SetMemberRole(ctx context.Context, actorID, subjectID string, role Role) error {
	if !role.Valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalidInput, role)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	actor, err := t.roleTx(ctx, tx, actorID)
	if err != nil {
		return err
	}
	current, err := t.roleTx(ctx, tx, subjectID)
	if err != nil {
		return err
	}
	if !actor.AtLeast(role) || !actor.AtLeast(current) {
		return ErrRoleAboveOwn
	}
	if current == role {
		return nil
	}
	if current == RoleOwner {
		if err := t.requireAnotherOwnerTx(ctx, tx, subjectID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memberships SET role = ? WHERE team = ? AND account_id = ?`,
		string(role), string(t.team), subjectID); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveMember takes subject out of t's team. Anyone may remove themselves;
// removing someone else needs an actor who outranks or equals them. The last
// owner stays.
func (t *Tenant) RemoveMember(ctx context.Context, actorID, subjectID string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	current, err := t.roleTx(ctx, tx, subjectID)
	if err != nil {
		return err
	}
	if actorID != subjectID {
		actor, err := t.roleTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !actor.AtLeast(current) {
			return ErrRoleAboveOwn
		}
	}
	if current == RoleOwner {
		if err := t.requireAnotherOwnerTx(ctx, tx, subjectID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memberships WHERE team = ? AND account_id = ?`,
		string(t.team), subjectID); err != nil {
		return err
	}
	return tx.Commit()
}

// safety: counting and then inserting is a race unless the team row is locked first; Postgres
// serializes on it and SQLite's single connection already serializes every writer.
func (t *Tenant) lockTeamTx(ctx context.Context, tx *storeTx) error {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT name FROM teams WHERE name = ?`+tx.forUpdate(), string(t.team)).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// CreateRunnerToken mints a runner token for t's team, refusing once the team
// holds [MaxRunnerTokensPerTeam] live ones. It returns the raw token once.
func (t *Tenant) CreateRunnerToken(
	ctx context.Context, principal string, scopes []string, createdBy string, now time.Time,
) (string, *Token, error) {
	if err := refuseAdminScope(scopes); err != nil {
		return "", nil, err
	}
	for attempt := 1; ; attempt++ {
		raw, tok, err := t.createRunnerTokenOnce(ctx, principal, scopes, createdBy, now)
		if err == nil {
			return raw, tok, nil
		}
		if attempt < mintAttempts && isTokenPrefixCollision(err) {
			continue
		}
		return "", nil, err
	}
}

func (t *Tenant) createRunnerTokenOnce(
	ctx context.Context, principal string, scopes []string, createdBy string, now time.Time,
) (string, *Token, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return "", nil, err
	}
	defer rollbackOrLog(tx)
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return "", nil, err
	}
	at := now.UTC().Unix()
	var live int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tokens
		WHERE team = ? AND kind = ? AND (revoked_at IS NULL OR revoked_at > ?) AND (expires_at IS NULL OR expires_at > ?)`,
		string(t.team), TokenKindRunner, at, at).Scan(&live); err != nil {
		return "", nil, err
	}
	if live >= MaxRunnerTokensPerTeam {
		return "", nil, ErrRunnerTokenLimit
	}
	raw, tok, err := createTokenRow(ctx, tx, t.team, principal, TokenKindRunner, scopes, 0, now,
		TokenOptions{CreatedBy: createdBy})
	if err != nil {
		return "", nil, err
	}
	return raw, tok, tx.Commit()
}

func (t *Tenant) roleTx(ctx context.Context, tx *storeTx, accountID string) (Role, error) {
	var role string
	err := tx.QueryRowContext(ctx,
		`SELECT role FROM memberships WHERE team = ? AND account_id = ?`, string(t.team), accountID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotMember
	}
	return Role(role), err
}

func (t *Tenant) requireAnotherOwnerTx(ctx context.Context, tx *storeTx, exceptID string) error {
	var owners int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memberships WHERE team = ? AND role = ? AND account_id <> ?`,
		string(t.team), string(RoleOwner), exceptID).Scan(&owners); err != nil {
		return err
	}
	if owners == 0 {
		return ErrLastOwner
	}
	return nil
}

// CreateInvitation offers email a role in t's team. The actor cannot invite
// above their own role, because an invitation is a grant and a second account
// invited at a higher role is the same escalation SetMemberRole refuses.
func (t *Tenant) CreateInvitation(ctx context.Context, actorID, email string, role Role, now time.Time) (Invitation, error) {
	email = NormalizeEmail(email)
	if !LooksLikeEmail(email) {
		return Invitation{}, fmt.Errorf("%w: that is not an email address", ErrInvalidInput)
	}
	if !role.Valid() {
		return Invitation{}, fmt.Errorf("%w: unknown role %q", ErrInvalidInput, role)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return Invitation{}, err
	}
	defer rollbackOrLog(tx)
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return Invitation{}, err
	}
	actor, err := t.roleTx(ctx, tx, actorID)
	if err != nil {
		return Invitation{}, err
	}
	if !actor.AtLeast(role) {
		return Invitation{}, ErrRoleAboveOwn
	}
	at := now.UTC().Unix()
	var open, today int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM invitations
		WHERE team = ? AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at > ?`,
		string(t.team), at).Scan(&open); err != nil {
		return Invitation{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM invitations WHERE team = ? AND created_at > ?`,
		string(t.team), at-int64((24*time.Hour).Seconds())).Scan(&today); err != nil {
		return Invitation{}, err
	}
	if open >= MaxOpenInvitations || today >= MaxInvitationsPerDay {
		return Invitation{}, ErrInvitationLimit
	}
	var already int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memberships m JOIN accounts a ON a.id = m.account_id
		WHERE m.team = ? AND a.email = ?`, string(t.team), email).Scan(&already); err != nil {
		return Invitation{}, err
	}
	if already > 0 {
		return Invitation{}, ErrAlreadyMember
	}
	// safety: an expired invitation still holds the open-invitation index, so it is withdrawn before a
	// fresh one to the same address is written; rows are kept, so the daily count stays honest.
	if _, err := tx.ExecContext(ctx, `
		UPDATE invitations SET withdrawn_at = ?
		WHERE team = ? AND email = ? AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at <= ?`,
		at, string(t.team), email, at); err != nil {
		return Invitation{}, err
	}
	id, err := newIdentityID()
	if err != nil {
		return Invitation{}, err
	}
	inv := Invitation{
		ID: id, Team: t.team, Email: email, Role: role, InvitedBy: actorID,
		CreatedAt: time.Unix(at, 0).UTC(), ExpiresAt: time.Unix(at, 0).UTC().Add(invitationTTL),
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO invitations (id, team, email, role, invited_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, string(t.team), inv.Email, string(inv.Role), actorID, at, inv.ExpiresAt.Unix()); err != nil {
		if isUniqueViolation(err) {
			return Invitation{}, ErrInvitationOpen
		}
		return Invitation{}, err
	}
	return inv, tx.Commit()
}

// Invitations lists t's open invitations.
func (t *Tenant) Invitations(ctx context.Context, now time.Time) (_ []Invitation, err error) {
	rows, err := t.s.query(ctx, invitationSelect+`
		WHERE i.team = ? AND i.accepted_at IS NULL AND i.withdrawn_at IS NULL AND i.expires_at > ?
		ORDER BY i.created_at`, string(t.team), now.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return scanInvitations(rows)
}

// DeleteInvitation withdraws one of t's open invitations. An id that is
// another team's, already accepted, or already withdrawn is ErrNotFound. The
// row stays, so a withdrawn invitation still counts toward the daily cap.
func (t *Tenant) DeleteInvitation(ctx context.Context, id string, now time.Time) error {
	res, err := t.s.exec(ctx, `
		UPDATE invitations SET withdrawn_at = ?
		WHERE team = ? AND id = ? AND accepted_at IS NULL AND withdrawn_at IS NULL`,
		now.UTC().Unix(), string(t.team), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	return nil
}

// RunnerTokens lists t's live runner tokens.
func (t *Tenant) RunnerTokens(ctx context.Context, now time.Time) (_ []Token, err error) {
	rows, err := t.s.query(ctx, `
		SELECT prefix, principal, scopes, created_by, created_at, expires_at, last_used_at
		FROM tokens
		WHERE team = ? AND kind = ? AND (revoked_at IS NULL OR revoked_at > ?)
		  AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at`, string(t.team), TokenKindRunner, now.UTC().Unix(), now.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	var out []Token
	for rows.Next() {
		tok := Token{Team: t.team, Kind: TokenKindRunner}
		var scopes string
		var created int64
		var expires, lastUsed sql.NullInt64
		if err := rows.Scan(&tok.Prefix, &tok.Principal, &scopes, &tok.CreatedBy, &created, &expires, &lastUsed); err != nil {
			return nil, err
		}
		tok.Scopes = splitScopes(scopes)
		tok.CreatedAt = time.Unix(created, 0).UTC()
		if expires.Valid {
			ts := time.Unix(expires.Int64, 0).UTC()
			tok.ExpiresAt = &ts
		}
		if lastUsed.Valid {
			ts := time.Unix(lastUsed.Int64, 0).UTC()
			tok.LastUsedAt = &ts
		}
		out = append(out, tok)
	}
	return out, rows.Err()
}

// RunnerToken reads one of t's runner tokens by prefix, or ErrNotFound.
func (t *Tenant) RunnerToken(ctx context.Context, prefix string) (Token, error) {
	tok := Token{Team: t.team, Kind: TokenKindRunner, Prefix: prefix}
	var revoked sql.NullInt64
	err := t.s.queryRow(ctx, `
		SELECT principal, created_by, revoked_at FROM tokens WHERE team = ? AND prefix = ? AND kind = ?`,
		string(t.team), prefix, TokenKindRunner).Scan(&tok.Principal, &tok.CreatedBy, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if revoked.Valid {
		ts := time.Unix(revoked.Int64, 0).UTC()
		tok.RevokedAt = &ts
	}
	return tok, err
}

// RevokeRunnerToken revokes one of t's runner tokens. A prefix that belongs
// to another team, or is already revoked, is ErrNotFound.
func (t *Tenant) RevokeRunnerToken(ctx context.Context, prefix string, now time.Time) error {
	at := now.UTC().Unix()
	res, err := t.s.exec(ctx, `
		UPDATE tokens SET revoked_at = ?
		WHERE team = ? AND prefix = ? AND kind = ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		at, string(t.team), prefix, TokenKindRunner, at)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	return nil
}
