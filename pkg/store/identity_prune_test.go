package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPruneSpentIdentityKeepsWhatStillAdmits(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	old := now.Add(-store.SpentIdentityRetention - time.Hour).Unix()
	recent := now.Add(-time.Hour).Unix()
	future := now.Add(time.Hour).Unix()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB().Exec(storetest.Rebind(st, q), args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, inv := range []struct {
		id                           string
		expires                      int64
		accepted, withdrawn, created any
	}{
		{"inv-old-accepted", future, old, nil, old},
		{"inv-old-expired", old, nil, nil, old},
		{"inv-recent-withdrawn", future, nil, recent, recent},
		{"inv-open", future, nil, nil, recent},
	} {
		exec(`INSERT INTO invitations (id, team, email, role, invited_by, created_at, expires_at, accepted_at, withdrawn_at)
		      VALUES (?, 'acme', ?, 'reader', 'owner', ?, ?, ?, ?)`,
			inv.id, inv.id+"@example.com", inv.created, inv.expires, inv.accepted, inv.withdrawn)
	}
	for _, tok := range []struct {
		prefix           string
		expires, revoked any
	}{
		{"tok-old-revoked", nil, old},
		{"tok-old-expired", old, nil},
		{"tok-recent-revoked", nil, recent},
		{"tok-live", future, nil},
		{"tok-forever", nil, nil},
	} {
		exec(`INSERT INTO tokens (hash, prefix, principal, kind, scopes, created_at, expires_at, revoked_at)
		      VALUES (?, ?, 'p', 'user', 'runs.read', ?, ?, ?)`,
			"hash-"+tok.prefix, tok.prefix, old, tok.expires, tok.revoked)
	}
	exec(`INSERT INTO sessions (hash, principal, scopes, created_at, expires_at) VALUES ('s-gone', 'p', '', ?, ?)`, old, recent)
	exec(`INSERT INTO sessions (hash, principal, scopes, created_at, expires_at) VALUES ('s-live', 'p', '', ?, ?)`, recent, future)

	pruned, err := st.PruneSpentIdentity(ctx, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned.Invitations != 2 || pruned.Tokens != 2 || pruned.Sessions != 1 {
		t.Fatalf("pruned %+v, want 2 invitations, 2 tokens, 1 session", pruned)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM invitations`); n != 2 {
		t.Fatalf("invitations left = %d, want the open and the recently withdrawn", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM tokens WHERE prefix LIKE 'tok-%'`); n != 3 {
		t.Fatalf("tokens left = %d, want the live, the unexpiring and the recently revoked", n)
	}
	again, err := st.PruneSpentIdentity(ctx, now)
	if err != nil || again != (store.IdentityPrune{}) {
		t.Fatalf("a second pass = %+v, %v; want nothing", again, err)
	}
}
