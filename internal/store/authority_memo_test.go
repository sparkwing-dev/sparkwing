package store

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func countAncestorReads(recording *authorizationConnection) *int {
	reads := new(int)
	recording.beforeQuery = func(query string) error {
		if strings.Contains(query, "SELECT digest,authority_json,") {
			*reads++
		}
		return nil
	}
	return reads
}

func TestWriteTransactionReadsEachAncestorTwice(t *testing.T) {
	s, recording := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	root := authorityRoot("root", "a")
	authorityPersist(t, s, root)
	child := authorityRoot("child", "a")
	child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	authorityPersist(t, s, child)
	s.dialect = DialectPostgres
	reads := countAncestorReads(recording)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		if _, err := tx.ResolveAuthority(t.Context(), ref); err != nil {
			return err
		}
		if *reads != 4 {
			t.Fatalf("first locked resolution read ancestry %d times, want discovery and verification of 2 ancestors (4)", *reads)
		}
		if _, err := tx.ResolveAuthority(t.Context(), ref); err != nil {
			return err
		}
		if _, err := authorityChainTx(t.Context(), tx, ref, false); err != nil {
			return err
		}
		if err := tx.CheckBinding(t.Context(), capability.Principal{Authority: ref}, capability.BindNone, capability.Target{Team: "a"}); err != nil {
			return err
		}
		if *reads != 4 {
			t.Fatalf("repeated resolutions read ancestry %d times in total, want 4", *reads)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnlockedWriteTransactionReadsAreNotReused(t *testing.T) {
	s, recording := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	authorityPersist(t, s, authorityRoot("root", "a"))
	s.dialect = DialectPostgres
	reads := countAncestorReads(recording)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		for range 2 {
			if _, err := authorityChainTx(t.Context(), tx, ref, false); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if *reads != 2 {
		t.Fatalf("unlocked write-transaction resolutions read the ancestor %d times, want each read fresh (2)", *reads)
	}
}

func requireUnauthenticated(t *testing.T, err error, what string) {
	t.Helper()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != hosterr.Unauthenticated {
		t.Fatalf("%s: got %v, want an unauthenticated refusal", what, err)
	}
}

func TestAuthorityMemoClearedByOwnRevocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke func(*storeTx, capability.Capability, capability.Capability) error
	}{
		{"leaf", func(tx *storeTx, _, leaf capability.Capability) error {
			return tx.operator.revokeAuthorityTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: leaf.Prefix})
		}},
		{"ancestor", func(tx *storeTx, root, _ capability.Capability) error {
			return tx.operator.revokeAuthorityTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix})
		}},
		{"rotation revoking the previous credential", func(tx *storeTx, _, leaf capability.Capability) error {
			return scheduleRotationTx(t.Context(), tx, leaf, tx.now(t.Context()))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := authorityStore(t)
			root := authorityRoot("root", "a")
			authorityPersist(t, s, root)
			leaf := authorityRoot("leaf", "a")
			leaf.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
			authorityPersist(t, s, leaf)
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: leaf.Prefix}
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				if _, err := tx.ResolveAuthority(t.Context(), ref); err != nil {
					return err
				}
				if err := tc.revoke(tx, root, leaf); err != nil {
					return err
				}
				_, err := tx.ResolveAuthority(t.Context(), ref)
				requireUnauthenticated(t, err, "resolution after this transaction's revocation")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthorityMemoNeverUpgradesALock(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("root", "a")
	root.Grants = []capability.Grant{capability.RunsRead}
	authorityPersist(t, s, root)
	leaf := authorityRoot("leaf", "a")
	leaf.Grants = []capability.Grant{capability.RunsRead}
	leaf.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	authorityPersist(t, s, leaf)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: leaf.Prefix}
	err := s.inTxOnce(t.Context(), func(tx *storeTx) error {
		if _, err := authorityChainTx(t.Context(), tx, ref, true); err != nil {
			return err
		}
		_, err := authorityChainTx(t.Context(), tx, ref, true, leaf.Prefix)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete authority lock set") {
		t.Fatalf("write lock after a shared lock on the same authority: %v, want the incomplete lock set refusal", err)
	}
}

func TestAuthorityMemoStartsEmptyAfterRestart(t *testing.T) {
	s, recording := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	authorityPersist(t, s, authorityRoot("root", "a"))
	s.dialect = DialectPostgres
	reads := countAncestorReads(recording)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}
	var perAttempt []int
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		before := *reads
		if _, err := tx.ResolveAuthority(t.Context(), ref); err != nil {
			return err
		}
		perAttempt = append(perAttempt, *reads-before)
		if len(perAttempt) == 1 {
			return errAuthorityChanged
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(perAttempt) != 2 || perAttempt[1] != 2 {
		t.Fatalf("ancestor reads per attempt %v, want the retry to discover and verify again (2)", perAttempt)
	}
}

func TestSessionAuthorityMemoKeyedByChildTeam(t *testing.T) {
	s := authorityStore(t)
	member := authorityMember(t, s, "member", RoleEditor)
	rewriteSchemaExec(t, s.db, "INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','member','reader',1)")
	rewriteSchemaExec(t, s.db, "INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('member','b',0)")
	for _, team := range []string{"a", "b"} {
		c := authorityRoot("child-"+team, team)
		c.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
		c.Grants = []capability.Grant{capability.RunsRead}
		c.Parent = &member.Authority
		authorityPersist(t, s, c)
	}
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		if err := tx.operator.lockAuthorityTx(t.Context(), tx, []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: "child-a"}, {Kind: capability.AuthorityCapability, ID: "child-b"}}); err != nil {
			return err
		}
		for _, team := range []string{"a", "b", "a"} {
			chain, err := authorityChainTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "child-" + team}, true)
			if err != nil {
				return err
			}
			if session := chain[1].principal.Capability; session.Team != team {
				t.Fatalf("child in team %s resolved its session in team %s", team, session.Team)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionAuthorityMemoKeyedByMemberToken(t *testing.T) {
	s := authorityStore(t)
	member := authorityMember(t, s, "member", RoleEditor)
	rewriteSchemaExec(t, s.db, "UPDATE sessions SET expires_at=1 WHERE hash='member'")
	token := authorityRoot("token", "a")
	token.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
	token.Grants = []capability.Grant{capability.RunsRead}
	token.Parent = &member.Authority
	expires := time.Now().Add(time.Hour)
	token.ExpiresAt = &expires
	persistWithOrigin(t, s, token, &CapabilityOrigin{Kind: capability.ProvisionMember, Requirement: &authorityRequirement{Account: "member"}})
	plain := token
	plain.Prefix, plain.Digest = "plain", [32]byte{1}
	authorityPersist(t, s, plain)
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		if _, err := authorityChainTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "token"}, true); err != nil {
			return err
		}
		_, err := authorityChainTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "plain"}, true)
		requireUnauthenticated(t, err, "plain delegate of an expired session after a member token")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func persistWithOrigin(t *testing.T, s *Store, c capability.Capability, origin *CapabilityOrigin) {
	t.Helper()
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return tx.operator.persistCapabilityTx(t.Context(), tx, c, origin)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityLockAfterLedgerIsRefused(t *testing.T) {
	s := authorityStore(t)
	authorityPersist(t, s, authorityRoot("root", "a"))
	member := authorityMember(t, s, "member", RoleEditor)
	for _, tc := range []struct {
		ref     capability.AuthorityRef
		refusal string
	}{
		{capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}, "authority lock order: authority must precede the ledger"},
		{member.Authority, "authority lock order: memberships must precede the ledger"},
	} {
		err := s.inTxOnce(t.Context(), func(tx *storeTx) error {
			if _, err := lockLedgerTx(t.Context(), tx, "a"); err != nil {
				return err
			}
			_, err := authorityChainTx(t.Context(), tx, tc.ref, true)
			return err
		})
		if err == nil || err.Error() != tc.refusal {
			t.Fatalf("%s locked after the ledger: %v, want %q", tc.ref.Kind, err, tc.refusal)
		}
	}
}

func TestAuthorityMemoKeepsNothingFromAChangedLockSet(t *testing.T) {
	s, recording := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)")
	member := authorityMember(t, s, "member", RoleEditor)
	rewriteSchemaExec(t, s.db, "INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','member','editor',1)")
	rewriteSchemaExec(t, s.db, "INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('member','b',0)")
	s.dialect = DialectPostgres
	selectTeam := func(team string) {
		t.Helper()
		if _, err := recording.Conn.(driver.ExecerContext).ExecContext(t.Context(), "UPDATE sessions SET team='"+team+"' WHERE hash='member'", nil); err != nil {
			t.Fatal(err)
		}
	}
	moved := false
	recording.beforeQuery = func(query string) error {
		if !moved && strings.HasSuffix(query, " FOR UPDATE") {
			moved = true
			selectTeam("b")
		}
		return nil
	}
	err := s.inTxOnce(t.Context(), func(tx *storeTx) error {
		if _, err := authorityChainTx(t.Context(), tx, member.Authority, true); !errors.Is(err, errAuthorityChanged) {
			t.Fatalf("session moved teams between discovery and verification: %v, want a restart", err)
		}
		selectTeam("a")
		chain, err := authorityChainTx(t.Context(), tx, member.Authority, false)
		if err != nil {
			return err
		}
		if team := chain[0].principal.Capability.Team; team != "a" {
			t.Fatalf("resolution after a refused lock set used team %s from that refused verification", team)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityMemoClearedByMixedCaseReturningWrite(t *testing.T) {
	s, recording := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	authorityPersist(t, s, authorityRoot("root", "a"))
	s.dialect = DialectPostgres
	reads := countAncestorReads(recording)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}
	if err := s.inTxOnce(t.Context(), func(tx *storeTx) error {
		if _, err := tx.ResolveAuthority(t.Context(), ref); err != nil {
			return err
		}
		var name string
		if err := tx.QueryRowContext(t.Context(), `UPDATE teams SET updated_at=updated_at WHERE name='a' Returning name`).Scan(&name); err != nil {
			return err
		}
		before := *reads
		if _, err := authorityChainTx(t.Context(), tx, ref, false); err != nil {
			return err
		}
		if *reads == before {
			t.Fatal("resolution after a Returning write reused ancestors verified before it")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRememberedAuthorityNodesShareNoMemory(t *testing.T) {
	var principal capability.Principal
	var requirement authorityRequirement
	fillEveryField(reflect.ValueOf(&principal).Elem())
	fillEveryField(reflect.ValueOf(&requirement).Elem())
	original := authorityNode{principal: principal, requirement: &requirement, memberToken: true}
	clone := cloneAuthorityNode(original)
	if !reflect.DeepEqual(original, clone) {
		t.Fatal("clone changed the node")
	}
	if shared := sharedMemory(reflect.ValueOf(original), reflect.ValueOf(clone), "node"); shared != "" {
		t.Fatalf("clone shares %s with the remembered node", shared)
	}
}

func fillEveryField(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillEveryField(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillEveryField(v.Index(0))
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Unix(1, 0)))
			return
		}
		for i := range v.NumField() {
			fillEveryField(v.Field(i))
		}
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	case reflect.Array:
		for i := range v.Len() {
			fillEveryField(v.Index(i))
		}
	case reflect.Uint8:
		v.SetUint(1)
	default:
		panic("fillEveryField: unhandled kind " + v.Kind().String())
	}
}

func sharedMemory(a, b reflect.Value, path string) string {
	if a.Type() == reflect.TypeFor[time.Time]() {
		return ""
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Slice:
		if !a.IsNil() && a.Pointer() == b.Pointer() {
			return path
		}
		if a.Kind() == reflect.Pointer {
			return sharedMemory(a.Elem(), b.Elem(), path)
		}
		for i := range a.Len() {
			if shared := sharedMemory(a.Index(i), b.Index(i), path); shared != "" {
				return shared
			}
		}
	case reflect.Map:
		panic("sharedMemory: maps are not cloned")
	case reflect.Struct:
		for i := range a.NumField() {
			if shared := sharedMemory(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); shared != "" {
				return shared
			}
		}
	}
	return ""
}

func TestOwnerSnapshotReadsEachAncestorOnce(t *testing.T) {
	tenant, actor := ownerPlacementFixture(t)
	trace := countClaimVisits(t, tenant.s, func(string) {}, nil)
	trace.allStatements, trace.enabled = true, true
	if _, err := tenant.ReadOwnerSnapshot(t.Context(), actor.Authority, "run"); err != nil {
		t.Fatal(err)
	}
	reads := 0
	for _, statement := range trace.statements {
		if strings.Contains(statement.query, "SELECT digest,authority_json,") {
			reads++
		}
	}
	if reads != 2 {
		t.Fatalf("owner snapshot read its two ancestors %d times, want once each", reads)
	}
}

func TestWriteTransactionCopyMarkedAsSnapshotKeepsNoReads(t *testing.T) {
	s, recording := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	authorityPersist(t, s, authorityRoot("held", "a"))
	authorityPersist(t, s, authorityRoot("other", "a"))
	s.dialect = DialectPostgres
	reads := countAncestorReads(recording)
	held := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "held"}
	other := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "other"}
	if err := s.inTxOnce(t.Context(), func(tx *storeTx) error {
		if _, err := tx.ResolveAuthority(t.Context(), held); err != nil {
			return err
		}
		// hack: this is the copy claimCapacityReadyTx makes of its claim transaction.
		probe := *tx
		probe.snapshotRead = true
		if _, err := authorityChainTx(t.Context(), &probe, other, false); err != nil {
			return err
		}
		before := *reads
		if _, err := tx.ResolveAuthority(t.Context(), other); err != nil {
			return err
		}
		if *reads == before {
			t.Fatal("locked resolution trusted an unlocked read made through a copy of its own write transaction")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
