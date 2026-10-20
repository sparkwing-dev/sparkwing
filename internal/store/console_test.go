package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func consoleFixture(t *testing.T) (*Store, *Console, capability.Authorized) {
	t.Helper()
	s := authorityStore(t)
	owner := authorityMember(t, s, "operator", RoleReader)
	c := s.AsConsole(func(id string) bool { return id == "operator" })
	a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialConsole, Authority: owner.Authority, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"}}}, Target: capability.Target{Team: "b"}}
	return s, c, a
}

func TestConsoleCommittingAuthorityProperty(t *testing.T) {
	s, c, base := consoleFixture(t)
	root := authorityRoot("root", "a")
	root.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "operator"}
	root.Grants = []capability.Grant{capability.RunsRead}
	authorityPersist(t, s, root)
	rewriteSchemaExec(t, s.db, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id,capability_prefix) SELECT 'derived',principal,scopes,created_at,expires_at,team,account_id,'root' FROM sessions WHERE hash='operator'`)
	writes := []func(capability.Authorized) error{
		func(a capability.Authorized) error {
			_, err := c.ConsoleRefund(t.Context(), a, controllerwire.OperatorRefund{PaymentID: "pi_test", RefundID: "re_test", AmountCents: 1, Reason: "support"}, "once")
			return err
		},
		func(a capability.Authorized) error {
			_, err := c.ConsoleGrantCredit(t.Context(), a, CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "console:ref", CreatedBy: "forged", Reason: "test"})
			return err
		},
		func(a capability.Authorized) error {
			_, err := c.ConsoleSetBillingTrust(t.Context(), a, BillingTrustChange{Trust: BillingTrustGranted, Reason: "test"})
			return err
		},
		func(a capability.Authorized) error { _, err := c.ConsoleFreeze(t.Context(), a, "test"); return err },
		func(a capability.Authorized) error {
			_, err := c.ConsoleDeclineStandingRequest(t.Context(), a, "standing-x", "test")
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != apierr.Forbidden && refusal.Code != apierr.Unauthenticated {
				return nil
			}
			return err
		},
		func(a capability.Authorized) error {
			_, err := c.ConsoleUnfreeze(t.Context(), a, "", "test", false)
			return err
		},
		func(a capability.Authorized) error { _, err := c.ConsoleDeleteTeam(t.Context(), a); return err },
		func(a capability.Authorized) error {
			a.Target.AccountID = "victim@example.test"
			_, err := c.ConsoleDeleteAccount(t.Context(), a)
			return err
		},
		func(a capability.Authorized) error {
			_, err := c.ConsoleApproveWaitlist(t.Context(), a, []string{"waiting"}, 0, "once")
			return err
		},
		func(a capability.Authorized) error {
			_, err := c.ConsoleSetCreditSettings(t.Context(), a, CreditSettingsUpdate{})
			return err
		},
		func(a capability.Authorized) error {
			_, err := c.ConsoleUnbindGitHubInstallation(t.Context(), a, 1)
			return err
		},
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO standing_requests(id,team,tier,message,requested_by,requested_at,state) VALUES ('standing-x','b','High','m','owner',1,'open')`)
	property := func(phase uint8, input []byte) bool {
		a := base
		c.allowed = func(string) bool { return true }
		rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=NULL,expires_at=? WHERE hash='operator'`, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
		switch phase % 8 {
		case 0:
			a.Principal.Authority.ID = "derived"
		case 1:
			a.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
		case 2:
			a.Principal.Capability.Subject.ID = fmt.Sprintf("other-%x", sha256.Sum256(input))
		case 3:
			a.Principal.Class = capability.CredentialSession
		case 4:
			c.allowed = func(string) bool { return false }
		case 5:
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=1 WHERE hash='operator'`)
		case 6:
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET expires_at=1 WHERE hash='operator'`)
		case 7:
			a.Principal.Authority.ID = "missing"
		}
		for _, write := range writes {
			if err := write(a); err == nil {
				return false
			}
		}
		var count int
		err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM credit_grants)+(SELECT COUNT(*) FROM credit_freezes)+(SELECT COUNT(*) FROM team_deletions)+(SELECT COUNT(*) FROM business_events)+(SELECT COUNT(*) FROM standing_requests WHERE state<>'open')`).Scan(&count)
		return err == nil && count == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	c.allowed = func(id string) bool { return id == "operator" }
	rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=NULL,expires_at=? WHERE hash='operator'`, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	if declined, err := c.ConsoleDeclineStandingRequest(t.Context(), base, "standing-x", "valid"); err != nil || declined.State != StandingRequestDeclined {
		t.Fatalf("valid operator decline: %+v %v", declined, err)
	}
}

func TestConsoleWritesRecheckConfigurationBeforeCommit(t *testing.T) {
	s, c, a := consoleFixture(t)
	calls := 0
	c.allowed = func(string) bool { calls++; return calls == 1 }
	if _, err := c.ConsoleFreeze(t.Context(), a, "test"); err == nil {
		t.Fatal("removed operator committed a hold")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM credit_freezes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("hold survived rollback: %d %v", count, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM business_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit survived rollback: %d %v", count, err)
	}
}

func TestConsoleNativeFinanceAndStableWaitlistBatch(t *testing.T) {
	s, c, a := consoleFixture(t)
	ctx := t.Context()
	grant, err := c.ConsoleGrantCredit(ctx, a, CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 10, Reference: "console:once", CreatedBy: "spoof", Reason: "test"})
	if err != nil || grant.Grant.CreatedBy != "operator" {
		t.Fatalf("grant: %+v %v", grant, err)
	}
	replay, err := c.ConsoleGrantCredit(ctx, a, CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 10, Reference: "console:once", Reason: "test"})
	if err != nil || replay.Created || replay.Grant.ID != grant.Grant.ID {
		t.Fatalf("grant replay: %+v %v", replay, err)
	}
	if _, err := c.ConsoleSetBillingTrust(ctx, a, BillingTrustChange{Trust: BillingTrustGranted, Reason: "review", Actor: "spoof"}); err != nil {
		t.Fatal(err)
	}
	freeze, err := c.ConsoleFreeze(ctx, a, "review")
	if err != nil || !freeze.Frozen || len(freeze.Disputes) != 1 {
		t.Fatalf("freeze: %+v %v", freeze, err)
	}
	if err := s.inTx(ctx, func(tx *storeTx) error {
		_, err := holdCreditFreezeTx(ctx, tx, "b", "dispute", "", "test", "", tx.now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	freeze, err = c.ConsoleUnfreeze(ctx, a, "", "resolved", false)
	if err != nil || len(freeze.Disputes) != 1 || freeze.Disputes[0] != "dispute" {
		t.Fatalf("operator unfreeze: %+v %v", freeze, err)
	}
	if _, err := c.ConsoleUnfreeze(ctx, a, "absent", "resolved", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent hold: %v", err)
	}
	freeze, err = c.ConsoleUnfreeze(ctx, a, "dispute", "resolved", false)
	if err != nil || freeze.Frozen {
		t.Fatalf("explicit unfreeze: %+v %v", freeze, err)
	}
	for i := range 3 {
		rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at,waitlisted_at) VALUES (?,?,1,1,?)`, fmt.Sprintf("wait-%d", i), fmt.Sprintf("wait-%d@example.test", i), i+1)
	}
	first, err := c.ConsoleApproveWaitlist(ctx, a, nil, 1, "batch")
	if err != nil || len(first) != 1 || first[0].ID != "wait-0" {
		t.Fatalf("approval: %+v %v", first, err)
	}
	second, err := c.ConsoleApproveWaitlist(ctx, a, nil, 1, "batch")
	if err != nil || len(second) != 0 {
		t.Fatalf("approval replay: %+v %v", second, err)
	}
	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE waitlisted_at<>0`).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("replayed batch admitted next account: %d %v", remaining, err)
	}
	if _, err := c.ConsoleApproveWaitlist(ctx, a, nil, 2, "batch"); err == nil {
		t.Fatal("rebound approval key")
	}
}

func TestConsoleNativeDeletionRevokesCapabilityEnvelopes(t *testing.T) {
	s, c, a := consoleFixture(t)
	root := authorityRoot("runner", "b")
	authorityPersist(t, s, root)
	if _, err := c.ConsoleDeleteTeam(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	var revoked int64
	if err := s.db.QueryRow(`SELECT revoked_at FROM capabilities WHERE prefix='runner'`).Scan(&revoked); err != nil || revoked == 0 {
		t.Fatalf("native root survived deletion: %d %v", revoked, err)
	}
	tx, err := s.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	if _, err := loadCapabilityTx(t.Context(), tx, root.Prefix); err != nil {
		t.Fatalf("revocation corrupted envelope: %v", err)
	}
}

func TestConsoleNativeAccountDeletion(t *testing.T) {
	s, c, a := consoleFixture(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('victim','victim@example.test',1,1)`)
	root := authorityRoot("created-by-victim", "b")
	authorityPersist(t, s, root)
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by='victim' WHERE prefix=?`, root.Prefix)
	a.Target.AccountID = "victim@example.test"
	result, err := c.ConsoleDeleteAccount(t.Context(), a)
	if err != nil || result.AccountID != "victim" {
		t.Fatalf("delete: %+v %v", result, err)
	}
	if _, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}); err == nil {
		t.Fatal("deleted account's credential survived")
	}
}

func TestDeletionActorCannotBePresentedByCaller(t *testing.T) {
	s := authorityStore(t)
	tx, err := s.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	if err := prepareStateActorTx(context.Background(), tx, "a", "run", Actor{Kind: ActorOperator}, tx.now()); err == nil {
		t.Fatal("unverified internal actor acquired authority")
	}
}

func TestConsoleTeamDeletionUsesNativeCancellation(t *testing.T) {
	tenant, now, _ := stateFixture(t)
	s := tenant.s
	owner := authorityMember(t, s, "operator", RoleReader)
	rewriteSchemaExec(t, s.db, `UPDATE memberships SET team='b' WHERE account_id='operator'`)
	rewriteSchemaExec(t, s.db, `UPDATE sessions SET team='b' WHERE hash='operator'`)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=? WHERE team='a'`, now.Add(time.Minute).UnixNano())
	a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialConsole, Authority: owner.Authority, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"}}}, Target: capability.Target{Team: "a"}}
	c := s.AsConsole(func(id string) bool { return id == "operator" })
	if _, err := c.ConsoleDeleteTeam(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Status != StatusRunning || row.ClaimedBy != "runner" || row.Generation != 2 || row.Attempt != 3 || row.CancelReason != "team_deleted" || row.CancelRequestedAt == nil {
		t.Fatalf("holder rights: %+v %v", row, err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.FinishedAt != nil || run.CancelRequestedAt == nil || run.CancelReason != "team_deleted" {
		t.Fatalf("run cancellation: %+v %v", run, err)
	}
}

func TestConsoleGlobalAdmissionAuditDeduplicatesAndDeletes(t *testing.T) {
	s, c, a := consoleFixture(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('victim','victim@example.test',1,1)`)
	for range 2 {
		if err := s.inTx(t.Context(), func(tx *storeTx) error { return recordAdmissionEventTx(tx, "victim", "waitlist", "operator", tx.now()) }); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM business_events WHERE team IS NULL AND account='victim'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("global audit: %d %v", count, err)
	}
	a.Target.AccountID = "victim"
	if _, err := c.ConsoleDeleteAccount(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM business_events WHERE account='victim'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted audit: %d %v", count, err)
	}
}

func TestConsoleNoFaultReleaseBelongsToTheOperatorWhoPlacedTheHold(t *testing.T) {
	s, c, alice := consoleFixture(t)
	bobActor := authorityMember(t, s, "bob", RoleReader)
	bob := alice
	bob.Principal.Authority = bobActor.Authority
	bob.Principal.Capability.Subject.ID = "bob"
	c.allowed = func(id string) bool { return id == "operator" || id == "bob" }
	ctx := t.Context()
	reason := strings.Repeat("€", 300)
	held, err := c.ConsoleFreeze(ctx, alice, reason)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConsoleFreeze(ctx, bob, "bob's review"); err != nil {
		t.Fatal(err)
	}
	var stored, placer string
	if err := s.db.QueryRow(`SELECT reason, placed_by FROM credit_freezes WHERE dispute_id=?`, held.Disputes[0]).Scan(&stored, &placer); err != nil || stored != reason || placer != "operator" {
		t.Fatalf("stored reason intact=%v placer=%q: %v", stored == reason, placer, err)
	}
	for _, release := range []struct {
		hold string
		who  capability.Authorized
	}{{held.Disputes[0], bob}, {"", alice}, {"", bob}} {
		_, err := c.ConsoleUnfreeze(ctx, release.who, release.hold, "forgive", true)
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.Forbidden {
			t.Fatalf("no-fault release of another operator's hold %q: %v", release.hold, err)
		}
	}
	var open int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM credit_freezes WHERE released_at IS NULL`).Scan(&open); err != nil || open != 2 {
		t.Fatalf("refused releases changed holds: %d %v", open, err)
	}
	if _, err := c.ConsoleUnfreeze(ctx, alice, held.Disputes[0], "forgive", true); err != nil {
		t.Fatalf("placing operator's no-fault release: %v", err)
	}
	var noFault int
	if err := s.db.QueryRow(`SELECT no_fault FROM credit_freezes WHERE dispute_id=?`, held.Disputes[0]).Scan(&noFault); err != nil || noFault != 1 {
		t.Fatalf("no_fault=%d %v", noFault, err)
	}
}
