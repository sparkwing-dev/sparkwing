package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func sshCredential(fingerprint string) store.GitCredential {
	return store.GitCredential{
		Host: "gitlab.example.com", Kind: store.GitCredentialSSH, Secret: "enc:key",
		KnownHosts: "gitlab.example.com ssh-ed25519 AAAA", Fingerprint: fingerprint, CreatedBy: "owner",
	}
}

// release is a release to a runner holding a live claim on runID's trigger,
// which it creates in tn's team, for a minute from now.
func release(t *testing.T, st *store.Store, tn *store.Tenant, runID string) store.GitCredentialRelease {
	t.Helper()
	ctx := context.Background()
	if err := tn.CreateTrigger(ctx, store.Trigger{ID: runID, Pipeline: "build", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	claimant := mintTeamClaimant(t, tn, "agent:"+runID)
	if _, err := st.ClaimSpecificTriggerFor(ctx, runID, claimant, time.Minute); err != nil {
		t.Fatal(err)
	}
	return store.GitCredentialRelease{RunID: runID, Runner: "runner:pool", TokenPrefix: claimant.TokenPrefix, Claimant: claimant}
}

// An ssh credential is released only after its owner confirms the pinned
// host key, and every release leaves an audit row.
func TestGitCredentialIsReleasedOnlyOnceConfirmed(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	rel1 := release(t, st, acme, "run-1")

	stored, err := acme.PutGitCredential(ctx, sshCredential("SHA256:one"), now)
	if err != nil || stored.ConfirmedAt != nil || stored.Team != "acme" {
		t.Fatalf("put = %+v, %v; want an unconfirmed acme credential", stored, err)
	}
	if _, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", rel1, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("release of an unconfirmed credential = %v, want ErrNotFound", err)
	}
	if _, err := acme.ConfirmGitCredential(ctx, "gitlab.example.com", "SHA256:other", now); !errors.Is(err, store.ErrFingerprintMismatch) {
		t.Fatalf("confirming another fingerprint = %v, want ErrFingerprintMismatch", err)
	}
	confirmed, err := acme.ConfirmGitCredential(ctx, "gitlab.example.com", "SHA256:one", now)
	if err != nil || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirm = %+v, %v", confirmed, err)
	}
	got, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", rel1, now)
	if err != nil || got.Secret != "enc:key" || got.KnownHosts == "" {
		t.Fatalf("release = %+v, %v", got, err)
	}
	rels, err := acme.GitCredentialReleases(ctx, 10)
	if err != nil || len(rels) != 1 {
		t.Fatalf("releases = %+v, %v; want one audit row", rels, err)
	}
	if r := rels[0]; r.Team != "acme" || r.CredentialID != stored.ID || r.RunID != "run-1" ||
		r.Runner != "runner:pool" || r.TokenPrefix != rel1.TokenPrefix || r.Host != "gitlab.example.com" {
		t.Fatalf("audit row = %+v", r)
	}
}

func TestGitCredentialHTTPSIsConfirmedWhenStored(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	rel1 := release(t, st, acme, "run-1")
	c, err := acme.PutGitCredential(ctx, store.GitCredential{
		Host: "bitbucket.org", Kind: store.GitCredentialHTTPS, Username: "x-token-auth", Secret: "enc:tok",
	}, time.Now())
	if err != nil || c.ConfirmedAt == nil {
		t.Fatalf("put https = %+v, %v; want confirmed", c, err)
	}
	if _, err := acme.ReleaseGitCredential(ctx, "bitbucket.org", rel1, time.Now()); err != nil {
		t.Fatalf("release = %v", err)
	}
}

// Replacing the key keeps the credential usable only while the host key it
// pins is the one the owner already confirmed.
func TestGitCredentialReplacementKeepsOnlyAConfirmedHostKey(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	rel2, rel3 := release(t, st, acme, "run-2"), release(t, st, acme, "run-3")
	if _, err := acme.PutGitCredential(ctx, sshCredential("SHA256:one"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.ConfirmGitCredential(ctx, "gitlab.example.com", "SHA256:one", now); err != nil {
		t.Fatal(err)
	}
	rotated := sshCredential("SHA256:one")
	rotated.Secret = "enc:rotated"
	c, err := acme.PutGitCredential(ctx, rotated, now)
	if err != nil || c.ConfirmedAt == nil {
		t.Fatalf("rotation under the same host key = %+v, %v; want still confirmed", c, err)
	}
	if got, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", rel2, now); err != nil || got.Secret != "enc:rotated" {
		t.Fatalf("release after rotation = %+v, %v; want the new key", got, err)
	}
	moved, err := acme.PutGitCredential(ctx, sshCredential("SHA256:two"), now)
	if err != nil || moved.ConfirmedAt != nil {
		t.Fatalf("a new host key = %+v, %v; want unconfirmed", moved, err)
	}
	if _, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", rel3, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("release after the host key changed = %v, want ErrNotFound until confirmed", err)
	}
	if list, err := acme.GitCredentials(ctx); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v; want the one credential for the host", list, err)
	}
}

// A deleted credential is never released, and another team neither reads nor
// releases, nor deletes, a team's credential.
func TestGitCredentialIsTheTeamsAloneAndGoneOnceDeleted(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	now := time.Now()
	rel1, rel2, relX := release(t, st, acme, "run-1"), release(t, st, acme, "run-2"), release(t, st, other, "run-x")
	c := store.GitCredential{Host: "gitlab.example.com", Kind: store.GitCredentialHTTPS, Secret: "enc:tok"}
	if _, err := acme.PutGitCredential(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	if _, err := other.GitCredentialForHost(ctx, "gitlab.example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another team reads acme's credential: %v", err)
	}
	if _, err := other.ReleaseGitCredential(ctx, "gitlab.example.com", relX, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another team releases acme's credential: %v", err)
	}
	if err := other.DeleteGitCredential(ctx, "gitlab.example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another team deletes acme's credential: %v", err)
	}
	if _, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", rel1, now); err != nil {
		t.Fatalf("control: acme's release before the delete = %v", err)
	}
	if err := acme.DeleteGitCredential(ctx, "gitlab.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", rel2, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("release after delete = %v, want ErrNotFound", err)
	}
	if rels, err := acme.GitCredentialReleases(ctx, 10); err != nil || len(rels) != 1 {
		t.Fatalf("releases = %d, %v; want only the one before the delete", len(rels), err)
	}
	if rels, err := other.GitCredentialReleases(ctx, 10); err != nil || len(rels) != 0 {
		t.Fatalf("the other team sees %d of acme's releases, %v", len(rels), err)
	}
}

func TestGitCredentialMachineOptInIsForTheTeamsOwnRunnerTokens(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	now := time.Now()
	_, tok, err := acme.CreateRunnerToken(ctx, "agent:laptop", []string{"nodes.claim"}, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetGitCredentialMachine(ctx, tok.Prefix, true, "intruder", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another team opts in acme's machine = %v, want ErrNotFound", err)
	}
	if on, err := acme.GitCredentialMachine(ctx, tok.Prefix); err != nil || on {
		t.Fatalf("before the opt-in = %v, %v", on, err)
	}
	if err := acme.SetGitCredentialMachine(ctx, tok.Prefix, true, "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := acme.SetGitCredentialMachine(ctx, tok.Prefix, true, "owner", now); err != nil {
		t.Fatalf("opting in twice = %v", err)
	}
	if on, err := acme.GitCredentialMachine(ctx, tok.Prefix); err != nil || !on {
		t.Fatalf("after the opt-in = %v, %v", on, err)
	}
	if on, err := other.GitCredentialMachine(ctx, tok.Prefix); err != nil || on {
		t.Fatalf("the other team sees acme's opt-in = %v, %v", on, err)
	}
	if err := acme.SetGitCredentialMachine(ctx, tok.Prefix, false, "owner", now); err != nil {
		t.Fatal(err)
	}
	if on, err := acme.GitCredentialMachine(ctx, tok.Prefix); err != nil || on {
		t.Fatalf("after opting out = %v, %v", on, err)
	}
	if err := acme.RevokeRunnerToken(ctx, tok.Prefix, now); err != nil {
		t.Fatal(err)
	}
	if err := acme.SetGitCredentialMachine(ctx, tok.Prefix, true, "owner", now.Add(time.Second)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("opting in a revoked token = %v, want ErrNotFound", err)
	}
}

func TestRotateGitCredentialSecretsRewritesEveryTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	teams := map[string]*store.Tenant{"acme": teamHandle(t, st, "acme"), "other": teamHandle(t, st, "other")}
	for team, tn := range teams {
		if _, err := tn.PutGitCredential(ctx, store.GitCredential{
			Host: "gitlab.example.com", Kind: store.GitCredentialHTTPS, Secret: "old:" + team,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.RotateGitCredentialSecrets(ctx, func(c store.GitCredential) (string, error) {
		return "new:" + string(c.Team), nil
	})
	if err != nil || n != 2 {
		t.Fatalf("rotate = %d, %v", n, err)
	}
	for team, tn := range teams {
		c, err := tn.GitCredentialForHost(ctx, "gitlab.example.com")
		if err != nil || c.Secret != "new:"+team {
			t.Fatalf("%s after rotation = %q, %v", team, c.Secret, err)
		}
	}
}

// The release re-checks the claim inside its own transaction, so a lease
// that lapses after the caller's check, or a claimant that never held the
// run, releases nothing and leaves no audit row.
func TestReleaseGitCredentialRefusesAClaimThatLapsedBeforeTheCommit(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	c := store.GitCredential{Host: "gitlab.example.com", Kind: store.GitCredentialHTTPS, Secret: "enc:tok"}
	if _, err := acme.PutGitCredential(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	rel := release(t, st, acme, "run-1")
	if _, err := st.ClaimedRunFor(ctx, "run-1", rel.Claimant, now); err != nil {
		t.Fatalf("control: the caller's own check passes = %v", err)
	}
	if _, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", rel, now); err != nil {
		t.Fatalf("control: release under the live claim = %v", err)
	}
	if _, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", rel, now.Add(2*time.Minute)); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("release once the lease lapsed = %v, want ErrClaimNotLive", err)
	}
	stranger := rel
	stranger.Claimant = store.ClaimIdentity{Principal: "agent:stranger", TokenPrefix: "swr_stranger"}
	if _, err := acme.ReleaseGitCredential(ctx, "gitlab.example.com", stranger, now); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("release to a runner that holds no claim = %v, want ErrClaimNotLive", err)
	}
	if rels, err := acme.GitCredentialReleases(ctx, 10); err != nil || len(rels) != 1 {
		t.Fatalf("releases = %d, %v; want only the one under the live claim", len(rels), err)
	}
}
