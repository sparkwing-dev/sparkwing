package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func gh(sub, email string) store.SignInProfile {
	return githubProfile(sub, email, time.Now().AddDate(-1, 0, 0))
}

func resolve(t *testing.T, st *store.Store, p store.SignInProfile) store.SignInResult {
	t.Helper()
	res, err := st.ResolveSignIn(context.Background(), p, store.SignUpConditions{}, time.Now())
	if err != nil {
		t.Fatalf("ResolveSignIn(%s %s): %v", p.Provider, p.Subject, err)
	}
	return res
}

func identitiesOf(t *testing.T, st *store.Store, accountID string) []store.Identity {
	t.Helper()
	ids, err := st.AccountIdentities(context.Background(), accountID)
	if err != nil {
		t.Fatalf("AccountIdentities: %v", err)
	}
	return ids
}

// A linked sign-in reaches the account whatever address the provider holds,
// and neither the link nor a later sign-in through it moves the account's
// email or withdraws another account's claim on the linked address.
func TestLinkIdentityAttachesASignInWhateverItsEmail(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "g-owner", "owner@example.com")
	holder := signIn(t, st, "g-holder", "elsewhere@example.org")

	// Control: before the link, this GitHub account is a stranger to owner.
	stranger := resolve(t, st, gh("gh-stranger", "elsewhere@example.org"))
	if stranger.Account.ID == owner.Account.ID {
		t.Fatal("an unlinked GitHub sign-in reached the owner's account")
	}

	linked, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-1", "Elsewhere@Example.org"), time.Now())
	if err != nil {
		t.Fatalf("LinkIdentity: %v", err)
	}
	if linked.Provider != store.ProviderGitHub || linked.Subject != "gh-1" || !linked.Linked ||
		linked.Email != "elsewhere@example.org" {
		t.Fatalf("linked = %+v", linked)
	}
	back := resolve(t, st, gh("gh-1", "elsewhere@example.org"))
	if back.Account.ID != owner.Account.ID || back.NewAccount {
		t.Fatalf("GitHub sign-in after linking = %+v, want the owner's account", back)
	}
	acct, err := st.Account(ctx, owner.Account.ID)
	if err != nil || acct.Email != "owner@example.com" || !acct.EmailVerified {
		t.Fatalf("owner after linking and signing in = %+v, %v; want its email unchanged", acct, err)
	}
	other, err := st.Account(ctx, holder.Account.ID)
	if err != nil || !other.EmailVerified {
		t.Fatalf("the account holding the linked address = %+v, %v; want its claim kept", other, err)
	}
	if ids := identitiesOf(t, st, owner.Account.ID); len(ids) != 2 {
		t.Fatalf("owner identities = %+v, want google and github", ids)
	}
}

// A sign-in already attached to any account, this one included, is refused
// and nothing changes on either side.
func TestLinkIdentityRefusesASignInAttachedAnywhere(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "g-owner", "owner@example.com")
	other := resolve(t, st, gh("gh-other", "other@example.com"))

	_, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-other", "other@example.com"), time.Now())
	if !errors.Is(err, store.ErrIdentityLinkedElsewhere) {
		t.Fatalf("linking another account's sign-in = %v, want ErrIdentityLinkedElsewhere", err)
	}
	if ids := identitiesOf(t, st, owner.Account.ID); len(ids) != 1 {
		t.Fatalf("owner identities after a refusal = %+v", ids)
	}
	if ids := identitiesOf(t, st, other.Account.ID); len(ids) != 1 || ids[0].Subject != "gh-other" {
		t.Fatalf("other identities after a refusal = %+v", ids)
	}
	if back := resolve(t, st, gh("gh-other", "other@example.com")); back.Account.ID != other.Account.ID {
		t.Fatal("the refused sign-in moved to the owner's account")
	}

	if _, err := st.LinkIdentity(ctx, owner.Account.ID, googleProfile("g-owner", "owner@example.com"), time.Now()); !errors.Is(err, store.ErrIdentityAlreadyLinked) {
		t.Fatalf("linking the account's own sign-in = %v, want ErrIdentityAlreadyLinked", err)
	}
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-2", "two@example.com"), time.Now()); err != nil {
		t.Fatalf("control: linking a free GitHub sign-in = %v", err)
	}
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-3", "three@example.com"), time.Now()); !errors.Is(err, store.ErrProviderAlreadyLinked) {
		t.Fatalf("linking a second GitHub sign-in = %v, want ErrProviderAlreadyLinked", err)
	}
	if ids := identitiesOf(t, st, owner.Account.ID); len(ids) != 2 {
		t.Fatalf("owner identities = %+v, want google and gh-2", ids)
	}
}

func TestUnlinkIdentityKeepsTheLastSignInMethod(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "g-owner", "owner@example.com")
	if _, _, err := st.UnlinkIdentity(ctx, owner.Account.ID, store.ProviderGoogle, "", time.Now()); !errors.Is(err, store.ErrLastSignInMethod) {
		t.Fatalf("unlinking the only sign-in = %v, want ErrLastSignInMethod", err)
	}
	if back := signIn(t, st, "g-owner", "owner@example.com"); back.Account.ID != owner.Account.ID {
		t.Fatal("the refused unlink detached the sign-in")
	}
	if _, _, err := st.UnlinkIdentity(ctx, owner.Account.ID, store.ProviderGitHub, "", time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unlinking a provider the account lacks = %v, want ErrNotFound", err)
	}
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-1", "gh@example.com"), time.Now()); err != nil {
		t.Fatal(err)
	}
	// Control: with a second method in place, the same unlink succeeds.
	if _, _, err := st.UnlinkIdentity(ctx, owner.Account.ID, store.ProviderGoogle, "", time.Now()); err != nil {
		t.Fatalf("unlinking google beside github = %v", err)
	}
}

func TestConcurrentUnlinksKeepOneMethodPostgres(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx := context.Background()
	owner := signIn(t, st, "g-owner", "owner@example.com")
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-1", "gh@example.com"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE FUNCTION wait_identity_delete() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN PERFORM pg_advisory_xact_lock(74120912); RETURN OLD; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER wait_identity_delete BEFORE DELETE ON identities
		FOR EACH ROW EXECUTE FUNCTION wait_identity_delete()`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	gate, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if _, err := gate.ExecContext(ctx, `SELECT pg_advisory_xact_lock(74120912)`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, provider := range []string{store.ProviderGoogle, store.ProviderGitHub} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := st.UnlinkIdentity(ctx, owner.Account.ID, provider, "", time.Now())
			results <- err
		}()
	}
	close(start)
	for {
		var waiting int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND (query LIKE '%DELETE FROM identities%'
			OR query LIKE '%accounts WHERE id%') AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("timed out waiting for concurrent unlinks")
		}
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	var succeeded, last int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrLastSignInMethod):
			last++
		default:
			t.Fatalf("concurrent unlink: %v", err)
		}
	}
	if succeeded != 1 || last != 1 || len(identitiesOf(t, st, owner.Account.ID)) != 1 {
		t.Fatalf("unlinks: %d succeeded, %d refused, identities = %+v", succeeded, last, identitiesOf(t, st, owner.Account.ID))
	}
}

func TestUnlinkBeforeIdentitySessionCreationRefusesStaleResolution(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "g-owner", "owner@example.com")
	profile := gh("gh-1", "gh@example.com")
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, profile, time.Now()); err != nil {
		t.Fatal(err)
	}
	resolved := resolve(t, st, profile)
	if _, _, err := st.UnlinkIdentity(ctx, owner.Account.ID, store.ProviderGitHub, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := st.CreateIdentityAccountSession(ctx, resolved.Account, resolved.Account.ActiveTeam,
		profile.Provider, profile.Subject, time.Hour, time.Now())
	if !errors.Is(err, store.ErrIdentityUnlinked) || raw != "" {
		t.Fatalf("session from stale identity resolution = %q, %v; want ErrIdentityUnlinked", raw, err)
	}
}

func TestUnlinkRevokesConcurrentIdentitySessionPostgres(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner := signIn(t, st, "g-owner", "owner@example.com")
	profile := gh("gh-1", "gh@example.com")
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, profile, time.Now()); err != nil {
		t.Fatal(err)
	}
	resolved := resolve(t, st, profile)
	if _, err := st.DB().Exec(`CREATE FUNCTION wait_session_insert() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN PERFORM pg_advisory_xact_lock(74120911); RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER wait_session_insert BEFORE INSERT ON sessions
		FOR EACH ROW EXECUTE FUNCTION wait_session_insert()`); err != nil {
		t.Fatal(err)
	}
	gate, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if _, err := gate.ExecContext(ctx, `SELECT pg_advisory_xact_lock(74120911)`); err != nil {
		t.Fatal(err)
	}
	type sessionResult struct {
		raw string
		err error
	}
	sessionDone := make(chan sessionResult, 1)
	go func() {
		raw, _, _, err := st.CreateIdentityAccountSession(ctx, resolved.Account, resolved.Account.ActiveTeam,
			profile.Provider, profile.Subject, time.Hour, time.Now())
		sessionDone <- sessionResult{raw, err}
	}()
	waitForDBLock := func(query string) {
		t.Helper()
		for {
			var waiting int
			err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND query LIKE $1 AND pid <> pg_backend_pid()`, query).Scan(&waiting)
			if err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for database lock")
			default:
			}
		}
	}
	waitForDBLock("%INSERT INTO sessions%")
	unlinkDone := make(chan error, 1)
	go func() {
		_, _, err := st.UnlinkIdentity(ctx, owner.Account.ID, store.ProviderGitHub, "", time.Now())
		unlinkDone <- err
	}()
	// With the account lock, unlink waits for the session transaction. Without
	// it, unlink can finish while session insertion is held at the trigger.
	unlinked := false
	for !unlinked {
		select {
		case err := <-unlinkDone:
			if err != nil {
				t.Fatal(err)
			}
			unlinked = true
		default:
		}
		var waiting int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%accounts WHERE id%'
			AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("timed out waiting for unlink")
		}
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	session := <-sessionDone
	if !unlinked {
		if err := <-unlinkDone; err != nil {
			t.Fatal(err)
		}
	}
	if session.err != nil {
		t.Fatalf("session creation: %v", session.err)
	}
	if _, err := st.LookupSession(session.raw, time.Now()); err == nil {
		t.Fatal("session created during unlink survived revocation")
	}
}

func TestSignInAndUnlinkUseTheSameLockOrderPostgres(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	profile := googleProfile("g-owner", "owner@example.com")
	owner := signIn(t, st, profile.Subject, profile.Email)
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-1", "gh@example.com"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE FUNCTION wait_identity_update() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN PERFORM pg_advisory_xact_lock(74120913); RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER wait_identity_update AFTER UPDATE ON identities
		FOR EACH ROW EXECUTE FUNCTION wait_identity_update()`); err != nil {
		t.Fatal(err)
	}
	gate, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if _, err := gate.ExecContext(ctx, `SELECT pg_advisory_xact_lock(74120913)`); err != nil {
		t.Fatal(err)
	}
	resolveDone := make(chan error, 1)
	go func() {
		_, err := st.ResolveSignIn(ctx, profile, store.SignUpConditions{}, time.Now())
		resolveDone <- err
	}()
	for {
		var waiting int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%UPDATE identities SET email%'
			AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("timed out waiting for identity update")
		}
	}
	unlinkDone := make(chan error, 1)
	go func() {
		_, _, err := st.UnlinkIdentity(ctx, owner.Account.ID, store.ProviderGoogle, "", time.Now())
		unlinkDone <- err
	}()
	for {
		select {
		case err := <-unlinkDone:
			t.Fatalf("unlink finished before identity update resumed: %v", err)
		default:
		}
		var waiting int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND (query LIKE '%DELETE FROM identities%'
			OR query LIKE '%accounts WHERE id%') AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("timed out waiting for unlink")
		}
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-resolveDone; err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	if err := <-unlinkDone; err != nil {
		t.Fatalf("unlink: %v", err)
	}
}

func TestSignInRetriesWhenIdentityWasUnlinkedWhileWaitingPostgres(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	profile := googleProfile("g-owner", "owner@example.com")
	owner := signIn(t, st, profile.Subject, profile.Email)
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-1", "gh@example.com"), time.Now()); err != nil {
		t.Fatal(err)
	}
	gate, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	var id string
	if err := gate.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, owner.Account.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	type result struct {
		accountID string
		err       error
	}
	done := make(chan result, 1)
	go func() {
		resolved, err := st.ResolveSignIn(ctx, profile, store.SignUpConditions{}, time.Now())
		done <- result{resolved.Account.ID, err}
	}()
	for {
		var waiting int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%SELECT id FROM accounts WHERE id%'
			AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("timed out waiting for sign-in account lock")
		}
	}
	if _, err := gate.ExecContext(ctx, `DELETE FROM identities WHERE provider = $1 AND subject = $2 AND account_id = $3`,
		profile.Provider, profile.Subject, owner.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.ExecContext(ctx, `INSERT INTO identity_unlinks (provider, subject, account_id, unlinked_at)
		VALUES ($1, $2, $3, $4)`, profile.Provider, profile.Subject, owner.Account.ID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	resolved := <-done
	if resolved.err != nil || resolved.accountID == owner.Account.ID || resolved.accountID == "" {
		t.Fatalf("sign-in after unlink = %q, %v; want a new account", resolved.accountID, resolved.err)
	}
}

// After an unlink, the provider account signs in as a stranger, even when it
// asserts the account's own verified address, which would otherwise join it
// by the email rule. Unlinking ends the account's other sessions and keeps
// the one that asked.
func TestUnlinkedSignInNoLongerReachesTheAccount(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "g-owner", "owner@example.com")
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-1", "owner@example.com"), now); err != nil {
		t.Fatal(err)
	}
	if back := resolve(t, st, gh("gh-1", "owner@example.com")); back.Account.ID != owner.Account.ID {
		t.Fatal("control: the linked sign-in did not reach the account")
	}
	keep, _, _, err := st.CreateAccountSession(ctx, owner.Account, owner.PersonalTeam, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _, err := st.CreateAccountSession(ctx, owner.Account, owner.PersonalTeam, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}

	gone, ended, err := st.UnlinkIdentity(ctx, owner.Account.ID, store.ProviderGitHub, keep, now)
	if err != nil {
		t.Fatalf("UnlinkIdentity: %v", err)
	}
	if gone.Subject != "gh-1" || ended != 1 {
		t.Fatalf("unlink = %+v, ended %d sessions; want gh-1 and one session", gone, ended)
	}
	if _, err := st.LookupSession(keep, now); err != nil {
		t.Fatalf("the session that unlinked = %v, want it kept", err)
	}
	if _, err := st.LookupSession(other, now); err == nil {
		t.Fatal("another session of the account survived the unlink")
	}

	after := resolve(t, st, gh("gh-1", "owner@example.com"))
	if after.Account.ID == owner.Account.ID || !after.NewAccount {
		t.Fatalf("GitHub sign-in after unlinking = %+v, want a new account", after)
	}
	if back := signIn(t, st, "g-owner", "owner@example.com"); back.Account.ID != owner.Account.ID {
		t.Fatal("the remaining Google sign-in lost the account")
	}
}

// Linking a sign-in again after unlinking it lifts the unlink.
func TestRelinkingLiftsAnUnlink(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "g-owner", "owner@example.com")
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-1", "owner@example.com"), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UnlinkIdentity(ctx, owner.Account.ID, store.ProviderGitHub, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkIdentity(ctx, owner.Account.ID, gh("gh-1", "owner@example.com"), now); err != nil {
		t.Fatalf("linking again = %v", err)
	}
	if n := countWhere(t, st, "identity_unlinks", "account_id = '"+owner.Account.ID+"'"); n != 0 {
		t.Fatalf("%d unlink records survived linking again", n)
	}
	if back := resolve(t, st, gh("gh-1", "owner@example.com")); back.Account.ID != owner.Account.ID {
		t.Fatal("the relinked sign-in did not reach the account")
	}
}

func TestIdentityLinkStateIsUsedOnce(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	expires := now.Add(10 * time.Minute)
	fresh, err := st.ConsumeIdentityLinkState(ctx, "nonce-1", expires, now)
	if err != nil || !fresh {
		t.Fatalf("first use = %v, %v; want fresh", fresh, err)
	}
	again, err := st.ConsumeIdentityLinkState(ctx, "nonce-1", expires, now)
	if err != nil || again {
		t.Fatalf("second use = %v, %v; want refused", again, err)
	}
	other, err := st.ConsumeIdentityLinkState(ctx, "nonce-2", expires, now)
	if err != nil || !other {
		t.Fatalf("control: another nonce = %v, %v; want fresh", other, err)
	}
	if empty, err := st.ConsumeIdentityLinkState(ctx, "", expires, now); err != nil || empty {
		t.Fatalf("empty nonce = %v, %v; want refused", empty, err)
	}
	k1, err := st.IdentityLinkStateKey()
	if err != nil || len(k1) == 0 {
		t.Fatalf("IdentityLinkStateKey = %x, %v", k1, err)
	}
}

// Deleting an account relabels what it did under its own addresses. A linked
// sign-in's address was never one it acted under, so another account's rows
// under that address keep their name.
func TestDeletingAnAccountLeavesALinkedAddressAlone(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	leaver := signIn(t, st, "g-leaver", "leaver@example.com")
	holder := signIn(t, st, "g-holder", "holder@example.com")
	if _, err := st.LinkIdentity(ctx, leaver.Account.ID, gh("gh-1", "holder@example.com"), now); err != nil {
		t.Fatal(err)
	}
	space := tenant(t, st, holder.PersonalTeam)
	runCtx := store.WithCreatingPrincipal(ctx, "holder@example.com")
	if err := space.CreateTriggerWithRun(runCtx,
		store.Trigger{ID: "run-h", Pipeline: "build", CreatedAt: now, TriggerUser: "holder@example.com"},
		store.Run{ID: "run-h", Pipeline: "build", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteAccount(ctx, leaver.Account.ID, now); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if n := countWhere(t, st, "runs", "id = 'run-h' AND created_principal = 'holder@example.com'"); n != 1 {
		t.Fatal("deleting the account that linked holder's address relabeled holder's run")
	}
	if n := countWhere(t, st, "identity_unlinks", "account_id = '"+leaver.Account.ID+"'"); n != 0 {
		t.Fatal("the deleted account's unlink records survived it")
	}
	// The linked sign-in is free again once its account is gone.
	if again := resolve(t, st, gh("gh-1", "holder@example.com")); again.Account.ID != holder.Account.ID {
		t.Fatalf("GitHub sign-in after the deletion = %+v, want it to join holder by the email rule", again)
	}
}
