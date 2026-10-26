package store

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRetentionIdentityPreservesLiveAuthorityProperty(t *testing.T) {
	s, _, now := maintenanceStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','account@example.test',1,1)`)
	random := rand.New(rand.NewPCG(771, 204))
	kept := map[string]bool{}
	var expected int64
	for i := range 64 {
		id := fmt.Sprintf("session-%d", i)
		expired, parent := random.IntN(2) == 0, random.IntN(2) == 0
		at := now.Add(time.Duration(random.Int64N(int64(time.Hour))) + 1)
		if expired {
			at = now.Add(-time.Duration(random.Int64N(int64(time.Hour))))
		}
		kept[id] = !expired || parent
		if !kept[id] {
			expected++
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,'account','runs.read',1,?,'a','account')`, id, at.UnixNano())
		if parent {
			child := authorityRoot("child-"+id, "a")
			child.Parent = &capability.AuthorityRef{Kind: capability.AuthoritySession, ID: id}
			authorityPersist(t, s, child)
		}
	}
	for remaining := expected; remaining > 0; {
		result, err := s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Limit: 3})
		if err != nil || result.Sessions <= 0 || result.Sessions > 3 {
			t.Fatalf("bounded identity pass: %+v %v", result, err)
		}
		remaining -= result.Sessions
		var lost int
		if err := s.db.QueryRow(`SELECT count(*) FROM capabilities c WHERE c.parent_kind='session' AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.hash=c.parent_id)`).Scan(&lost); err != nil || lost != 0 {
			t.Fatalf("lost session ancestry: %d %v", lost, err)
		}
	}
	var count int64
	if err := s.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 64-expected {
		t.Fatalf("surviving sessions: %d %v", count, err)
	}
	for id, want := range kept {
		if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE hash=?`, id).Scan(&count); err != nil || (count == 1) != want {
			t.Fatalf("session %s survived=%d, want %v: %v", id, count, want, err)
		}
	}
	again, err := s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Limit: 3})
	if err != nil || again.Sessions != 0 {
		t.Fatalf("repeated identity pass: %+v %v", again, err)
	}
}

func TestRetentionInvitationsKeepsWhatStillAdmits(t *testing.T) {
	s, _, now := maintenanceStore(t)
	old := now.Add(-31 * 24 * time.Hour).UnixNano()
	recent, future := now.Add(-time.Hour).UnixNano(), now.Add(time.Hour).UnixNano()
	for _, row := range []struct {
		id                  string
		expires             int64
		accepted, withdrawn any
	}{
		{"old-accepted", future, old, nil},
		{"old-expired", old, nil, nil},
		{"old-withdrawn", future, nil, old},
		{"recent-withdrawn", future, nil, recent},
		{"recent-expired", recent, nil, nil},
		{"open", future, nil, nil},
	} {
		acceptedBy := ""
		if row.accepted != nil {
			acceptedBy = "account"
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO invitations(id,team,email,role,invited_by,created_at,expires_at,accepted_at,accepted_by,withdrawn_at) VALUES (?,'a',?,'reader','owner',?,?,?,?,?)`, row.id, row.id+"@example.test", old, row.expires, row.accepted, acceptedBy, row.withdrawn)
	}
	for range 3 {
		result, err := s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Limit: 1})
		if err != nil || result.Invitations != 1 {
			t.Fatalf("invitation expiry: %+v %v", result, err)
		}
	}
	again, err := s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Limit: 10})
	if err != nil || again.Invitations != 0 {
		t.Fatalf("repeated invitation expiry: %+v %v", again, err)
	}
	rewriteSchemaCount(t, s.db, "invitations", 3)
}

func TestRetentionIdentityRollsBackAndRetries(t *testing.T) {
	s, _, now := maintenanceStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','account@example.test',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('expired','account','runs.read',1,?,'a','account')`, now.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO invitations(id,team,email,role,invited_by,created_at,expires_at) VALUES ('expired','a','expired@example.test','reader','owner',1,1)`)
	operator := s.AsOperator()
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_identity_retention BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT,'identity unavailable'); END`)
	result, err := operator.MaintainRetention(t.Context(), RetentionRequest{Limit: 10})
	if err == nil || result != (RetentionResult{}) {
		t.Fatalf("failed retention returned committed counters: %+v %v", result, err)
	}
	rewriteSchemaCount(t, s.db, "invitations", 1)
	rewriteSchemaCount(t, s.db, "sessions", 1)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_identity_retention`)
	if _, err := operator.MaintainRetention(t.Context(), RetentionRequest{Limit: 10}); err != nil {
		t.Fatalf("retried retention: %v", err)
	}
	rewriteSchemaCount(t, s.db, "invitations", 0)
	rewriteSchemaCount(t, s.db, "sessions", 0)
}

func TestInvitationPersistenceUsesNanoseconds(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	*now = now.Add(123456789 * time.Nanosecond)
	authorityMember(t, s, "owner", RoleOwner)
	invitation, err := tenant.InviteForTest(t, t.Context(), "owner", "invited@example.test", RoleReader, *now)
	if err != nil {
		t.Fatal(err)
	}
	if !invitation.CreatedAt.Equal(*now) || !invitation.ExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("invitation timestamps: %+v", invitation)
	}
	s.SetTestClock(t, func() time.Time { return *now })
	listed, err := s.AsOperator().OpenInvitationsForEmail(t.Context(), invitation.Email)
	if err != nil || len(listed) != 1 || !listed[0].CreatedAt.Equal(*now) || !listed[0].ExpiresAt.Equal(invitation.ExpiresAt) {
		t.Fatalf("invitation read: %+v %v", listed, err)
	}
	s.SetTestClock(t, func() time.Time { return *now })
	if claimed, err := s.AsOperator().ClaimInvitationEmail(t.Context(), invitation.ID); err != nil || !claimed {
		t.Fatalf("invitation email: %v %v", claimed, err)
	}
	var created, expires, emailed, sent int64
	if err := s.db.QueryRow(`SELECT created_at,expires_at,emailed_at FROM invitations WHERE id=?`, invitation.ID).Scan(&created, &expires, &emailed); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT sent_at FROM invitation_email_log`).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	if created != now.UnixNano() || expires != invitation.ExpiresAt.UnixNano() || emailed != created || sent != created {
		t.Fatalf("invitation persistence: created=%d expires=%d emailed=%d sent=%d", created, expires, emailed, sent)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,email_verified,created_at,updated_at) VALUES ('invited',?,1,1,1)`, invitation.Email)
	rewriteSchemaExec(t, s.db, `INSERT INTO identities(provider,subject,account_id,email,created_at) VALUES ('google','invited','invited',?,1)`, invitation.Email)
	raw, _, _, err := s.AsOperator().CreateIdentityAccountSession(t.Context(), Account{ID: "invited"}, "", ProviderGoogle, "invited", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)})
	if err != nil {
		t.Fatal(err)
	}
	route, _ := capability.LookupRoute("POST", "/api/v1/invitations/{invitation}/accept")
	accepted, err := s.AsOperator().AcceptAccountInvitation(t.Context(), capability.Authorized{Principal: p, Access: route.Access[0]}, invitation.ID)
	if err != nil || accepted.Team != tenant.team {
		t.Fatalf("invitation acceptance: %+v %v", accepted, err)
	}
	var at int64
	if err := s.db.QueryRow(`SELECT accepted_at FROM invitations WHERE id=?`, invitation.ID).Scan(&at); err != nil || at != created {
		t.Fatalf("acceptance timestamp: %d %v", at, err)
	}
	withdrawn, err := tenant.InviteForTest(t, t.Context(), "owner", "withdrawn@example.test", RoleReader, *now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.WithdrawInvitationForTest(t, t.Context(), withdrawn.ID, *now); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT withdrawn_at FROM invitations WHERE id=?`, withdrawn.ID).Scan(&at); err != nil || at != created {
		t.Fatalf("withdrawal timestamp: %d %v", at, err)
	}
}
