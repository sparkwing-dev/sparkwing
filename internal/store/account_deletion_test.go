package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestOwnAccountDeletionRequiresRecentSignIn(t *testing.T) {
	for _, age := range []time.Duration{10 * time.Minute, 10*time.Minute + time.Nanosecond} {
		t.Run(age.String(), func(t *testing.T) {
			s, a, raw, now := accountMutationFixture(t)
			plan, err := s.AsOperator().PreviewAccountDeletion(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(age)
			_, err = s.AsOperator().DeleteOwnAccount(t.Context(), a, plan.AccountID, []string{string(plan.Teams[0])})
			if age == 10*time.Minute {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Details["reason"] != apierr.Reason("reauth_required") {
				t.Fatalf("stale sign-in deletion = %v, want reauth_required", err)
			}
			if _, err := s.AsOperator().LookupSession(t.Context(), raw); err != nil {
				t.Fatalf("stale refusal revoked session: %v", err)
			}
			if _, err := s.AsOperator().Account(t.Context(), plan.AccountID); err != nil {
				t.Fatalf("stale refusal removed account: %v", err)
			}
		})
	}
}

func TestOwnAccountDeletionNamesMissingConfirmationList(t *testing.T) {
	s, a, _, _ := accountMutationFixture(t)
	_, err := s.AsOperator().DeleteOwnAccount(t.Context(), a, a.Principal.Capability.Subject.ID, nil)
	if err == nil || !strings.Contains(err.Error(), "confirm_teams is required") {
		t.Fatalf("missing list = %v", err)
	}
}

func TestOwnAccountDeletionRechecksScopeAndRevokesAllSessions(t *testing.T) {
	s, a, raw, _ := accountMutationFixture(t)
	id := a.Principal.Capability.Subject.ID
	plan, err := s.AsOperator().PreviewAccountDeletion(t.Context(), a)
	if err != nil || len(plan.Teams) != 1 {
		t.Fatalf("preview=%+v: %v", plan, err)
	}
	second, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), Account{ID: id}, plan.Teams[0], time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().CreateAccountTeam(t.Context(), a, TeamInfo{Slug: "another-team"}); err != nil {
		t.Fatal(err)
	}
	p0, err := s.AsOperator().ResolveAuthority(t.Context(), a.Principal.Authority)
	if err != nil {
		t.Fatal(err)
	}
	a.Principal = p0
	if _, err := s.AsOperator().DeleteOwnAccount(t.Context(), a, id, []string{string(plan.Teams[0])}); err == nil {
		t.Fatal("stale preview deleted an unconfirmed team")
	}
	if _, err := s.AsOperator().LookupSession(t.Context(), raw); err != nil {
		t.Fatalf("refusal revoked original session: %v", err)
	}
	// safety: team creation selected another team for the original session, so resolve its current authority again.
	p, err := s.AsOperator().ResolveAuthority(t.Context(), a.Principal.Authority)
	if err != nil {
		t.Fatal(err)
	}
	a.Principal = p
	plan, err = s.AsOperator().PreviewAccountDeletion(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := []string{}
	for _, team := range plan.Teams {
		confirmed = append(confirmed, string(team))
	}
	if _, err := s.AsOperator().DeleteOwnAccount(t.Context(), a, id, confirmed); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{raw, second} {
		if _, err := s.AsOperator().LookupSession(t.Context(), session); err == nil {
			t.Fatal("deleted account retained a session")
		}
	}
	rows, err := s.db.Query(`SELECT actor,account,subject_id,attrs FROM audit_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var actor, account, subject, attrs string
		if err := rows.Scan(&actor, &account, &subject, &attrs); err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{actor, account, subject, attrs} {
			for _, private := range []string{"member@example.test", raw, second} {
				if strings.Contains(value, private) {
					t.Fatalf("audit retained personal value %q", private)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnAccountDeletionRollsBackWhenAuditFails(t *testing.T) {
	s, a, raw, _ := accountMutationFixture(t)
	plan, err := s.AsOperator().PreviewAccountDeletion(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_account_delete_audit BEFORE INSERT ON audit_events WHEN NEW.kind='account.deleted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if _, err := s.AsOperator().DeleteOwnAccount(t.Context(), a, plan.AccountID, []string{string(plan.Teams[0])}); err == nil {
		t.Fatal("unaudited account deletion succeeded")
	}
	if _, err := s.AsOperator().LookupSession(t.Context(), raw); err != nil {
		t.Fatalf("audit failure revoked session: %v", err)
	}
	if _, err := s.AsOperator().Account(t.Context(), plan.AccountID); err != nil {
		t.Fatalf("audit failure removed account: %v", err)
	}
}

func TestOwnAccountDeletionRequiresLiveDirectAuthority(t *testing.T) {
	for _, change := range []string{"subject", "bearer", "target", "revoked"} {
		t.Run(change, func(t *testing.T) {
			s, a, _, now := accountMutationFixture(t)
			plan, err := s.AsOperator().PreviewAccountDeletion(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "subject":
				a.Principal.Capability.Subject.ID = "someone-else"
			case "bearer":
				a.Principal.Class = capability.CredentialBearer
			case "target":
				a.Target.AccountID = "someone-else"
			case "revoked":
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=? WHERE hash=?`, now.UnixNano(), a.Principal.Authority.ID)
			}
			if _, err := s.AsOperator().DeleteOwnAccount(t.Context(), a, plan.AccountID, []string{string(plan.Teams[0])}); err == nil {
				t.Fatal("invalid authority deleted an account")
			}
			if _, err := s.AsOperator().Account(t.Context(), plan.AccountID); err != nil {
				t.Fatalf("refusal removed account: %v", err)
			}
		})
	}
}
