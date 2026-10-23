package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modernsqlite "modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestIdentityLinkRechecksExpiryAfterLockWait(t *testing.T) {
	for _, operation := range []string{"consume", "link"} {
		for _, bound := range []string{"link_expiry", "recent_sign_in"} {
			t.Run(operation+"/"+bound, func(t *testing.T) {
				s, authorized, _, now := accountMutationFixture(t)
				claim := capability.IdentityLink{Authority: authorized.Principal.Authority, AccountID: authorized.Principal.Capability.Subject.ID, Provider: ProviderGitHub, Nonce: "waiting-link", ExpiresAt: now.Add(time.Hour)}
				elapsed := IdentityChangeSignInWindow + time.Nanosecond
				if bound == "link_expiry" {
					claim.ExpiresAt = now.Add(time.Minute)
					elapsed = time.Minute
				}
				if operation == "link" {
					if err := s.AsOperator().ConsumeIdentityLink(t.Context(), claim); err != nil {
						t.Fatal(err)
					}
				}
				connection := recordStoreFences(t, s)
				waited := false
				boundary := "SELECT id FROM accounts WHERE id="
				if operation == "link" {
					boundary = "SELECT binding,completed_at FROM flow_nonces "
				}
				connection.beforeLock = func(query string) {
					if strings.HasPrefix(query, boundary) && !waited {
						waited = true
						*now = now.Add(elapsed)
					}
				}
				var mutationErr error
				if operation == "consume" {
					mutationErr = s.AsOperator().ConsumeIdentityLink(t.Context(), claim)
				} else {
					_, mutationErr = s.AsOperator().LinkAccountIdentity(t.Context(), claim, SignInProfile{Provider: ProviderGitHub, Subject: "linked-account", Email: "linked@example.test", EmailVerified: true})
				}
				if !waited || mutationErr == nil {
					t.Fatalf("waited=%v error=%v", waited, mutationErr)
				}
				var effects int
				query := `SELECT COUNT(*) FROM flow_nonces WHERE kind='identity_link' AND nonce=?`
				if operation == "link" {
					query += ` AND completed_at IS NOT NULL`
				}
				if err := s.db.QueryRow(query, claim.Nonce).Scan(&effects); err != nil || effects != 0 {
					t.Fatalf("committed link effects=%d error=%v", effects, err)
				}
				var identities int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM identities WHERE provider=? AND subject=?`, ProviderGitHub, "linked-account").Scan(&identities); err != nil || identities != 0 {
					t.Fatalf("committed identities=%d error=%v", identities, err)
				}
			})
		}
	}
}

func TestSessionClockFailureRemainsBackendFailure(t *testing.T) {
	s, _, session, _ := accountMutationFixture(t)
	connection := recordStoreFences(t, s)
	clockErr := errors.New("clock unavailable after reading session")
	queried := false
	connection.beforeQuery = func(query string) error {
		if strings.HasPrefix(query, "SELECT principal,COALESCE(team,") {
			queried = true
			s.pinnedClock = nil
		}
		if queried && query == databaseTimeSQL(DialectPostgres) {
			return clockErr
		}
		return nil
	}
	if _, err := s.AsOperator().LookupSession(t.Context(), session); !queried || !errors.Is(err, clockErr) || !errors.Is(err, ErrSessionBackend) {
		t.Fatalf("read session=%v lookup error=%v", queried, err)
	}
}

func TestFlowNonceRechecksExpiryAfterInsertWait(t *testing.T) {
	s, connection := singleCursorStore(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	s.clock = func() time.Time { return now }
	waited := false
	connection.beforeExec = func(query string) {
		if strings.HasPrefix(query, "INSERT INTO flow_nonces") {
			waited = true
			now = expires
		}
	}
	used, err := s.consumeFlowNonce(t.Context(), "github_app_connect", "waiting", expires)
	var refused *apierr.Error
	if !waited || used || !errors.As(err, &refused) || refused.Code != apierr.Forbidden {
		t.Fatalf("waited=%v used=%v error=%v", waited, used, err)
	}
	var consumed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM flow_nonces WHERE kind='github_app_connect' AND nonce='waiting'`).Scan(&consumed); err != nil || consumed != 0 {
		t.Fatalf("expired nonce commits=%d error=%v", consumed, err)
	}
}

func recordStoreFences(t *testing.T, s *Store) *authorizationConnection {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fences.db")
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	raw, err := (&modernsqlite.Driver{}).Open(path)
	if err != nil {
		t.Fatal(err)
	}
	connection := &authorizationConnection{Conn: raw}
	original := s.db
	s.db = sql.OpenDB(authorizationConnector{conn: connection})
	s.db.SetMaxOpenConns(1)
	replacement := s.db
	t.Cleanup(func() {
		if err := errors.Join(original.Close(), replacement.Close()); err != nil {
			t.Error(err)
		}
	})
	s.dialect = DialectPostgres
	return connection
}
