package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

// Team-wide ceilings. Every machine token multiplies each per-principal budget, and every invitation
// may become a mail the deployment sends, so a team holds a bounded number of both.
const (
	MaxRunnerTokensPerTeam = 100
	MaxInvitationsPerDay   = 100
	MaxOpenInvitations     = 50
)

// RunnerTokenLifetime is how long a team's machine token lives unused. Each use
// moves its expiry to this far past the use, so a machine that keeps working
// keeps its token, and one a forgotten machine still holds stops working on
// its own. The token reads the secrets of whatever run it claims.
const RunnerTokenLifetime = 90 * 24 * time.Hour

// MaxCreatedTeams bounds how many teams one user creates over the account's
// life, their personal space included, because each team is a tenant the
// deployment pays to hold and a deleted team's slug is never reused.
// Deleting a team does not give the creation back.
const MaxCreatedTeams = 3

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
	// ErrAccountExists refuses a first sign-in whose verified address an
	// existing account holds. It names none of that account's providers.
	ErrAccountExists    = errors.New("store: an account with this email already exists")
	ErrTeamLimit        = errors.New("store: this user has created as many teams as one user may")
	ErrRunnerTokenLimit = errors.New("store: this team holds as many machine tokens as a team may")
	// ErrAgentNameTaken refuses a machine token for a name another live machine
	// token of the team already carries, because a node selector and a
	// workload identity bound to name= would admit either machine.
	ErrAgentNameTaken  = errors.New("store: another live machine token of this team carries that agent name; revoke it before minting a new one")
	ErrInvitationLimit = errors.New("store: this team has sent as many invitations as it may for now")
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
	// Waitlisted is true while the account waits for admission: it holds no
	// personal space and creates no team, but it may join a team that
	// invites it.
	Waitlisted bool
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
	// ProviderAccountCreatedAt is when the provider account was opened, for a
	// provider that states it; GitHub does and Google does not.
	ProviderAccountCreatedAt time.Time
}

// SignInResult reports what a sign-in did.
type SignInResult struct {
	Account Account
	// NewAccount is true when the sign-in created the human.
	NewAccount bool
	// PersonalTeam names the team the sign-in created, if it created one.
	PersonalTeam Team
	// WaitlistReason is why a new account was placed on the waitlist, or
	// empty when it was admitted or already existed.
	WaitlistReason string
	// GateClosed is the gate this sign-up closed by crossing a velocity
	// limit, or nil when it closed nothing.
	GateClosed *SignUpGate
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

// safety: the cut lands on a rune boundary, so truncated text stays valid UTF-8 for PostgreSQL.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
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
		err := createTeamTx(ctx, tx, accountID, Team(candidate), displayName, accountID, now)
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

var errSignInIdentityChanged = errors.New("store: sign-in identity changed during resolution")

// ResolveSignIn turns an authenticated provider profile into an account and
// makes sure the account has a team to land in.
//
// The rule, in full:
//
//  1. An identity seen before belongs to the account it already belongs to.
//  2. Otherwise, when an existing account holds the address verified, holds
//     no identity from this provider and did not unlink this one, the
//     sign-in is refused with [ErrAccountExists]. The provider account
//     joins that account only through [Store.LinkAccountIdentity], which a signed-in
//     session drives.
//  3. Otherwise it becomes a new account.
//
// Rule 2 is the security property. A matching address proves only who holds
// the address now: a reassigned work address, or one typed unverified into a
// new provider account, would otherwise walk into the previous holder's
// teams. This store refuses an unverified address outright, because no route
// here can later prove one.
//
// An account left holding no membership, new or returning, gets a personal
// space: a team whose only member is its owner. The exception is a waitlisted
// account, which gets one only when an operator admits it.
//
// Only a new account meets the sign-up gate: c and the stored gate decide
// whether it is admitted or waitlisted. An account that already exists, or an
// identity that links to one, is never gated.
func (o *Operator) ResolveSignIn(ctx context.Context, p SignInProfile, c SignUpConditions) (SignInResult, error) {
	p.Email = NormalizeEmail(p.Email)
	switch {
	case p.Provider == "" || p.Subject == "":
		return SignInResult{}, errors.New("store: sign-in names no provider or subject")
	case !p.EmailVerified || !LooksLikeEmail(p.Email):
		return SignInResult{}, ErrUnverifiedEmail
	}
	// safety: the retry re-reads email claims, personal slugs, and identities
	// after a concurrent sign-in or unlink changes the first decision.
	var last error
	for range 5 {
		res, err := o.s.resolveSignInOnce(ctx, p, c)
		if err == nil || (!isUniqueViolation(err) && !errors.Is(err, ErrSlugTaken) && !errors.Is(err, errSignInIdentityChanged)) {
			return res, err
		}
		last = err
	}
	return SignInResult{}, last
}

func (s *Store) resolveSignInOnce(ctx context.Context, p SignInProfile, c SignUpConditions) (SignInResult, error) {
	var result SignInResult
	err := s.inTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = tx.operator.resolveSignInTx(ctx, tx, p, c, tx.time)
		return err
	})
	if err != nil {
		return SignInResult{}, err
	}
	return result, nil
}

func (op *Operator) resolveSignInTx(ctx context.Context, tx *storeTx, p SignInProfile, c SignUpConditions, now time.Time) (SignInResult, error) {
	at := now.UTC().UnixNano()

	var res SignInResult
	var accountID string
	var linked int
	err := tx.QueryRowContext(ctx,
		`SELECT account_id, linked FROM identities WHERE provider = ? AND subject = ?`,
		p.Provider, p.Subject).Scan(&accountID, &linked)
	switch {
	case err == nil:
		if err := lockAccountTx(ctx, tx, accountID); err != nil {
			return SignInResult{}, err
		}
		err = tx.QueryRowContext(ctx,
			`SELECT linked FROM identities WHERE provider = ? AND subject = ? AND account_id = ?`,
			p.Provider, p.Subject, accountID).Scan(&linked)
		if errors.Is(err, sql.ErrNoRows) {
			return SignInResult{}, errSignInIdentityChanged
		}
		if err != nil {
			return SignInResult{}, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE identities SET email = ?, email_verified = ? WHERE provider = ? AND subject = ?`,
			p.Email, boolInt(p.EmailVerified), p.Provider, p.Subject); err != nil {
			return SignInResult{}, fmt.Errorf("identity: refresh: %w", err)
		}
		// safety: the account's address is its linking and invitation key, so it follows what the
		// provider asserts now; an address left behind would let whoever holds it next walk in. A
		// linked identity was attached whatever its address, so its address is not the account's.
		if linked == 0 {
			if err := claimEmailTx(ctx, tx, accountID, p.Email, at); err != nil {
				return SignInResult{}, err
			}
		}
	case errors.Is(err, sql.ErrNoRows):
		// safety: rule 2, the account side must hold this address verified too.
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM accounts WHERE email = ? AND email_verified = 1`, p.Email).Scan(&accountID)
		if err == nil {
			var sameProvider, unlinked int
			if err := tx.QueryRowContext(ctx, `
				SELECT (SELECT COUNT(*) FROM identities WHERE account_id = ? AND provider = ?),
				       (SELECT COUNT(*) FROM identity_unlinks WHERE account_id = ? AND provider = ? AND subject = ?)`,
				accountID, p.Provider, accountID, p.Provider, p.Subject).Scan(&sameProvider, &unlinked); err != nil {
				return SignInResult{}, err
			}
			// safety: one provider account per human, so a second subject from the same provider on this
			// address is a recycled address or another person; it gets a fresh account, never this one.
			// A provider account this account unlinked is held to the same rule, or unlinking would undo
			// itself at the next sign-in.
			if sameProvider > 0 || unlinked > 0 {
				if err := releaseEmailTx(ctx, tx, p.Email, "", at); err != nil {
					return SignInResult{}, err
				}
				accountID, err = "", sql.ErrNoRows
			}
		}
		switch {
		case err == nil:
			return SignInResult{}, ErrAccountExists
		case errors.Is(err, sql.ErrNoRows):
			if accountID, err = newIdentityID(); err != nil {
				return SignInResult{}, err
			}
			decision, err := decideSignUpTx(ctx, tx, p, c, now)
			if err != nil {
				return SignInResult{}, fmt.Errorf("identity: sign-up gate: %w", err)
			}
			var waitlistedAt int64
			if decision.reason != "" {
				waitlistedAt = now.UnixNano()
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO accounts (id, email, email_verified, name, active_team, created_at, updated_at,
					waitlisted_at, waitlist_reason)
				VALUES (?, ?, ?, ?, '', ?, ?, ?, ?)`,
				accountID, p.Email, boolInt(p.EmailVerified), strings.TrimSpace(p.Name), at, at,
				waitlistedAt, decision.reason); err != nil {
				return SignInResult{}, fmt.Errorf("identity: create account: %w", err)
			}
			if decision.reason == "" {
				if err := recordAdmissionTx(ctx, tx, accountID, admissionSignUp, now); err != nil {
					return SignInResult{}, fmt.Errorf("identity: record admission: %w", err)
				}
			}
			res.NewAccount = true
			res.WaitlistReason, res.GateClosed = decision.reason, decision.tripped
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
	var waitlisted int64
	if err := tx.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM memberships WHERE account_id = ?),
		       COALESCE((SELECT waitlisted_at FROM accounts WHERE id = ?), 0)`,
		accountID, accountID).Scan(&members, &waitlisted); err != nil {
		return SignInResult{}, err
	}
	if members == 0 && waitlisted == 0 {
		slug, err := createPersonalTeamTx(ctx, tx, accountID, slugBase(p.Email), personalDisplayName(p), now)
		if err != nil {
			return SignInResult{}, err
		}
		res.PersonalTeam = slug
	}
	if err := tx.operator.settleActiveTeamTx(ctx, tx, accountID, at); err != nil {
		return SignInResult{}, err
	}
	if res.Account, err = accountTx(ctx, tx, accountID); err != nil {
		return SignInResult{}, err
	}
	return res, nil
}

func (op *Operator) settleActiveTeamTx(ctx context.Context, tx *storeTx, accountID string, at int64) error {
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

const accountSelect = `SELECT id, email, email_verified, name, active_team, created_at, waitlisted_at FROM accounts`

func scanAccount(row rowScanner) (Account, error) {
	var a Account
	var verified int
	var active string
	var created, waitlisted int64
	if err := row.Scan(&a.ID, &a.Email, &verified, &a.Name, &active, &created, &waitlisted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, ErrNotFound
		}
		return Account{}, err
	}
	a.EmailVerified = verified == 1
	a.ActiveTeam = Team(active)
	a.CreatedAt = time.Unix(0, created).UTC()
	a.Waitlisted = waitlisted != 0
	return a, nil
}

// Account reads one account.
func (o *Operator) Account(ctx context.Context, id string) (Account, error) {
	return scanAccount(o.s.queryRow(ctx, accountSelect+` WHERE id = ?`, id))
}

func createAccountTeamTx(ctx context.Context, tx *storeTx, accountID string, slug Team, displayName string, now time.Time) (TeamInfo, error) {
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
	var created int
	var waitlisted int64
	if err := tx.QueryRowContext(ctx, `SELECT teams_created, COALESCE(waitlisted_at, 0) FROM accounts WHERE id = ?`+tx.forUpdate(),
		accountID).Scan(&created, &waitlisted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TeamInfo{}, ErrNotFound
		}
		return TeamInfo{}, err
	}
	if waitlisted != 0 {
		return TeamInfo{}, ErrWaitlisted
	}
	if created >= MaxCreatedTeams {
		return TeamInfo{}, fmt.Errorf(
			"%w: one user creates at most %d teams, the personal team included, and this user has created %d",
			ErrTeamLimit, MaxCreatedTeams, created)
	}
	if err := createTeamTx(ctx, tx, accountID, slug, displayName, accountID, now); err != nil {
		return TeamInfo{}, err
	}
	return TeamInfo{Slug: slug, DisplayName: displayName, CreatedBy: accountID}, nil
}

func createTeamTx(ctx context.Context, tx *storeTx, accountID string, slug Team, displayName, actor string, now time.Time) error {
	var deleted int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_deletions WHERE slug = ?`, string(slug)).Scan(&deleted); err != nil {
		return err
	}
	if deleted > 0 {
		return ErrSlugTaken
	}
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
		string(slug), accountID, string(RoleOwner), now.UTC().UnixNano()); err != nil {
		return fmt.Errorf("identity: add owner: %w", err)
	}
	if err := tx.operator.RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: slug, Account: accountID, Kind: BusinessEventTeamCreated,
		SubjectID: string(slug), Actor: actor,
	}); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET teams_created = teams_created + 1 WHERE id = ?`, accountID)
	return err
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
func (o *Operator) AccountMemberships(ctx context.Context, accountID string) (_ []Membership, err error) {
	rows, err := o.s.query(ctx, `
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
func (o *Operator) OpenInvitationsForEmail(ctx context.Context, email string) (out []Invitation, err error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackOrLog(tx)

	rows, err := tx.QueryContext(ctx, invitationSelect+`
		WHERE i.email = ? AND i.accepted_at IS NULL AND i.withdrawn_at IS NULL AND i.expires_at > ?
		ORDER BY i.created_at`, NormalizeEmail(email), tx.time.UTC().UnixNano())
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
		inv.CreatedAt, inv.ExpiresAt = time.Unix(0, created).UTC(), time.Unix(0, expires).UTC()
		out = append(out, inv)
	}
	return out, rows.Err()
}

// InvitationAcceptance reports what accepting an invitation did.
type InvitationAcceptance struct {
	Team Team
	// Admitted is true when a waitlisted account joined, which counts toward
	// the sign-up gate's velocity limits as an admission.
	Admitted bool
	// GateClosed is the gate this acceptance closed by crossing a velocity
	// limit, or nil when it closed nothing.
	GateClosed *SignUpGate
}

func (op *Operator) acceptAccountInvitationTx(ctx context.Context, tx *storeTx, accountID, invitationID string, now time.Time) (InvitationAcceptance, error) {
	var none InvitationAcceptance
	at := now.UTC().UnixNano()

	var team, email, role, inviter string
	var expires int64
	var accepted, withdrawn sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT team, email, role, invited_by, expires_at, accepted_at, withdrawn_at FROM invitations WHERE id = ?`, invitationID).
		Scan(&team, &email, &role, &inviter, &expires, &accepted, &withdrawn)
	if errors.Is(err, sql.ErrNoRows) {
		return none, ErrNotFound
	}
	if err != nil {
		return none, err
	}
	acct, err := accountTx(ctx, tx, accountID)
	if err != nil {
		return none, err
	}
	// safety: the address check runs before the expiry and use checks, so a
	// holder of someone else's invitation learns nothing about its state.
	if !acct.EmailVerified || acct.Email != email {
		return none, ErrEmailMismatch
	}
	if accepted.Valid || withdrawn.Valid || expires <= now.UnixNano() {
		return none, ErrInvitationClosed
	}
	// safety: the gate row is locked before the member count and the admission
	// count are read, so concurrent acceptances cannot both slip under a limit.
	gate, err := lockSignUpGateTx(ctx, tx)
	if err != nil {
		return none, err
	}
	if err := checkTeamRoomTx(ctx, tx, Team(team), gate.Limits.FreeTeamMembers); err != nil {
		return none, err
	}
	var inviterRole Role
	if err := tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE team=? AND account_id=?`, team, inviter).Scan(&inviterRole); errors.Is(err, sql.ErrNoRows) {
		return none, ErrInvitationClosed
	} else if err != nil {
		return none, err
	}
	if !slices.Contains(ScopesForRole(inviterRole), ScopeTeamAdmin) {
		return none, ErrInvitationClosed
	}
	// safety: the update repeats the unaccepted predicate and counts what it
	// changed, because two callers that both read it open would otherwise both
	// take it.
	res, err := tx.ExecContext(ctx, `
		UPDATE invitations SET accepted_at = ?, accepted_by = ?
		WHERE id = ? AND team = ? AND accepted_at IS NULL AND withdrawn_at IS NULL`, now.UnixNano(), accountID, invitationID, team)
	if err != nil {
		return none, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return none, ErrInvitationClosed
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memberships (team, account_id, role, created_at) VALUES (?, ?, ?, ?)`,
		team, accountID, role, at); err != nil {
		if isUniqueViolation(err) {
			return none, ErrAlreadyMember
		}
		return none, err
	}
	out := InvitationAcceptance{Team: Team(team)}
	if acct.Waitlisted {
		if out.GateClosed, err = tripVelocityTx(ctx, tx, gate, now); err != nil {
			return none, err
		}
		if err := recordAdmissionTx(ctx, tx, accountID, admissionInvitation, now); err != nil {
			return none, err
		}
		out.Admitted = true
	}
	return out, nil
}

// safety: a team that has bought credits pays for what its members run, so
// only a team without them is held to the free member limit; members it
// already has are never removed.
func checkTeamRoomTx(ctx context.Context, tx *storeTx, team Team, limit int) error {
	if limit <= 0 {
		return nil
	}
	var members int
	var paid int64
	if err := tx.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM memberships WHERE team = ?),
		       COALESCE((SELECT SUM(amount_micro) FROM credit_grants WHERE team = ? AND kind IN (?, ?)), 0)`,
		string(team), string(team), CreditGrantPaid, CreditGrantReversal).Scan(&members, &paid); err != nil {
		return err
	}
	if members < limit {
		return nil
	}
	// safety: a team held over a disputed payment is held to the free member
	// limit, because the payment that lifted it may be taken back.
	if paid > 0 {
		freeze, err := teamCreditFreezeTx(ctx, tx, team)
		if err != nil {
			return err
		}
		if !freeze.Frozen {
			return nil
		}
	}
	return ErrTeamFull
}

// CreateAccountSession opens a session for a current team member.
func (o *Operator) CreateAccountSession(ctx context.Context, acct Account, team Team, ttl time.Duration) (string, string, *Session, error) {
	return o.createAccountSession(ctx, acct, team, "", "", ttl)
}

// CreateIdentityAccountSession opens a session while the provider identity remains linked.
func (o *Operator) CreateIdentityAccountSession(ctx context.Context, acct Account, team Team, provider, subject string, ttl time.Duration) (string, string, *Session, error) {
	if provider == "" || subject == "" {
		return "", "", nil, fmt.Errorf("%w: provider and subject required", ErrInvalidInput)
	}
	return o.createAccountSession(ctx, acct, team, provider, subject, ttl)
}

func (o *Operator) createAccountSession(ctx context.Context, acct Account, team Team, provider, subject string, ttl time.Duration) (string, string, *Session, error) {
	if acct.ID == "" || ttl <= 0 {
		return "", "", nil, fmt.Errorf("%w: account and positive ttl required", ErrInvalidInput)
	}
	raw, err := newSessionID()
	if err != nil {
		return "", "", nil, err
	}
	csrf, err := o.deriveCSRFToken(ctx, raw)
	if err != nil {
		return "", "", nil, err
	}
	var session *Session
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := tx.operator.discoverAccountTeams(ctx, tx, lockSet{}, acct.ID)
			if team != "" {
				set.rows = append(set.rows, rowLock{key: storeLockKey{tier: lockTeam, keys: [3]string{string(team)}}, optional: true})
			}
			return set
		}); err != nil {
			return err
		}
		if provider != "" {
			var linked string
			err := tx.QueryRowContext(ctx, `SELECT account_id FROM identities WHERE provider=? AND subject=? AND account_id=?`, provider, subject, acct.ID).Scan(&linked)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrIdentityUnlinked
			}
			if err != nil {
				return err
			}
		}
		var role Role
		var sessionTeam any
		if team == "" {
			if provider == "" {
				return ErrNotMember
			}
		} else {
			sessionTeam = string(team)
			if err := tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE team=? AND account_id=?`, team, acct.ID).Scan(&role); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					err = ErrNotMember
				}
				return err
			}
			if !role.Valid() {
				return ErrNotMember
			}
		}
		var email string
		if err := tx.QueryRowContext(ctx, `SELECT email FROM accounts WHERE id=?`, acct.ID).Scan(&email); err != nil {
			return err
		}
		now := tx.time
		expires := now.Add(ttl)
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(hash,team,principal,scopes,account_id,created_at,expires_at)
 VALUES (?,?,?,'',?,?,?)`, sessionDigest(raw), sessionTeam, email, acct.ID, now.UnixNano(), expires.UnixNano()); err != nil {
			return err
		}
		if err := tx.operator.recordSessionMembershipsTx(ctx, tx, sessionDigest(raw)); err != nil {
			return err
		}
		session = &Session{Now: now, ID: raw, Principal: email, Team: team, AccountID: acct.ID, Scopes: ScopesForRole(role), CSRFToken: csrf, CreatedAt: now, ExpiresAt: expires}
		return nil
	})
	if err != nil {
		return "", "", nil, err
	}
	return raw, csrf, session, nil
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

func (t *Tenant) moveSessionsOffTeamTx(ctx context.Context, tx *storeTx, accountID string, now time.Time) error {
	if err := tx.operator.settleActiveTeamTx(ctx, tx, accountID, now.UTC().UnixNano()); err != nil {
		return err
	}
	var active string
	if err := tx.QueryRowContext(ctx,
		`SELECT active_team FROM accounts WHERE id = ?`, accountID).Scan(&active); err != nil {
		return err
	}
	if active == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT hash FROM sessions WHERE account_id=? AND team=? AND capability_prefix=''`, accountID, t.team)
	if err != nil {
		return err
	}
	sessions, err := scanPrefixes(rows)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if err := (&Tenant{s: tx.store, team: Team(active)}).recordSessionMembershipTx(ctx, tx, session); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE sessions SET team = ? WHERE account_id = ? AND team = ? AND capability_prefix = ''`,
		active, accountID, string(t.team))
	return err
}

func scanPrefixes(rows *sql.Rows) (_ []string, err error) {
	defer closeRowsInto(rows, &err)
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, rows.Err()
}

// safety: counting and then inserting is a race unless the team row is locked first; Postgres
// serializes on it and SQLite's single connection already serializes every writer.
func (t *Tenant) lockTeamTx(ctx context.Context, tx *storeTx) error {
	err := acquireLockSet(ctx, tx, func() lockSet {
		var set lockSet
		set.add(lockTeam, true, string(t.team))
		return set
	})
	if errors.Is(err, ErrUnknownTeam) {
		return ErrNotFound
	}
	return err
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

func (t *Tenant) createInvitationTx(ctx context.Context, tx *storeTx, actorID, email string, role Role, id string, now time.Time) (Invitation, error) {
	actor, err := t.roleTx(ctx, tx, actorID)
	if err != nil {
		return Invitation{}, err
	}
	if !actor.AtLeast(role) {
		return Invitation{}, ErrRoleAboveOwn
	}
	at := now.UTC().UnixNano()
	var open, today int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM invitations
		WHERE team = ? AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at > ?`,
		string(t.team), at).Scan(&open); err != nil {
		return Invitation{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM invitations WHERE team = ? AND created_at > ?`,
		string(t.team), now.Add(-24*time.Hour).UnixNano()).Scan(&today); err != nil {
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
	inv := Invitation{
		ID: id, Team: t.team, Email: email, Role: role, InvitedBy: actorID,
		CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(invitationTTL),
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO invitations (id, team, email, role, invited_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, string(t.team), inv.Email, string(inv.Role), actorID, at, inv.ExpiresAt.UnixNano()); err != nil {
		if isUniqueViolation(err) {
			return Invitation{}, ErrInvitationOpen
		}
		return Invitation{}, err
	}
	return inv, nil
}

// Invitations lists t's open invitations.
func (t *Tenant) Invitations(ctx context.Context) (out []Invitation, err error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackOrLog(tx)

	rows, err := tx.QueryContext(ctx, invitationSelect+`
		WHERE i.team = ? AND i.accepted_at IS NULL AND i.withdrawn_at IS NULL AND i.expires_at > ?
		ORDER BY i.created_at`, string(t.team), tx.time.UTC().UnixNano())
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
