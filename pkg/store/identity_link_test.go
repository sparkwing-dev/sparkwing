package store_test

import (
	"context"
	"errors"
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
