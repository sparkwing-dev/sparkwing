package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func defaultTenant(t *testing.T, st *store.Store) *store.Tenant {
	t.Helper()
	return tenant(t, st, store.DefaultTeam)
}

func TestTeamTokenFamilyRefusesEmptyUnknownAndMixedScopes(t *testing.T) {
	for _, tc := range []struct {
		scopes []string
		want   store.TokenFamily
	}{
		{[]string{"runs.read"}, store.TokenFamilyMember},
		{[]string{"runs.read", "runs.write", "approvals.write"}, store.TokenFamilyMember},
		{[]string{"nodes.claim"}, store.TokenFamilyMachine},
		{[]string{"triggers.claim", "runs.state", "secrets.read", "logs.write"}, store.TokenFamilyMachine},
		{nil, ""},
		{[]string{"runs.state"}, ""},
		{[]string{"runs.read", "nodes.claim"}, ""},
		{[]string{"team.admin"}, ""},
		{[]string{"admin"}, ""},
		{[]string{"runs.read", "made.up"}, ""},
	} {
		got, err := store.TeamTokenFamily(tc.scopes)
		if tc.want == "" {
			if !errors.Is(err, store.ErrInvalidInput) {
				t.Errorf("TeamTokenFamily(%v) = %q, %v; want ErrInvalidInput", tc.scopes, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("TeamTokenFamily(%v) = %q, %v; want %q", tc.scopes, got, err, tc.want)
		}
	}
}

func TestTeamTokensCarryTheirOwnPrefixAndActAsTheirFamily(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tn := defaultTenant(t, st)
	seedMinter(t, st, store.DefaultTeam, "ed", store.RoleEditor)
	now := time.Now().UTC()
	memberRaw, member, err := tn.CreateMemberToken(ctx, "ed@example.com", []string{"runs.read"}, "ed", now)
	if err != nil {
		t.Fatal(err)
	}
	machineRaw, machine, err := tn.CreateMachineToken(ctx, "box", []string{"nodes.claim"}, "ed", now)
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]struct {
		tok    *store.Token
		acts   string
		family store.TokenFamily
	}{
		memberRaw:  {member, store.TokenKindUser, store.TokenFamilyMember},
		machineRaw: {machine, store.TokenKindRunner, store.TokenFamilyMachine},
	} {
		if !strings.HasPrefix(raw, store.TokenPrefixTeam+"_") || store.TokenKindFromPrefix(raw) != store.TokenKindTeam {
			t.Errorf("raw %q is not a team token", raw[:store.PrefixLen])
		}
		got, err := st.LookupToken(raw, now)
		if err != nil {
			t.Fatalf("a fresh team token does not authenticate: %v", err)
		}
		if got.Kind != store.TokenKindTeam || store.ActingKind(got.Kind, got.Principal) != want.acts || got.Family() != want.family {
			t.Errorf("%s acts as %q in family %q, want %q in %q", want.tok.Principal,
				store.ActingKind(got.Kind, got.Principal), got.Family(), want.acts, want.family)
		}
	}
	if machine.Principal != "agent:box" || member.Principal != "ed@example.com" {
		t.Errorf("principals = %q, %q", machine.Principal, member.Principal)
	}
}

func TestMemberTokenScopesAreBoundedByTheMintersRole(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tn := defaultTenant(t, st)
	seedMinter(t, st, store.DefaultTeam, "reader", store.RoleReader)
	seedMinter(t, st, store.DefaultTeam, "owner", store.RoleOwner)
	now := time.Now().UTC()
	if _, _, err := tn.CreateMemberToken(ctx, "r@example.com", store.CLITokenScopes(store.RoleReader), "reader", now); err != nil {
		t.Fatalf("a reader minting the reader's scopes = %v", err)
	}
	if _, _, err := tn.CreateMemberToken(ctx, "r@example.com", []string{"runs.read", "runs.write"}, "reader", now); !errors.Is(err, store.ErrRoleAboveOwn) {
		t.Fatalf("a reader minting runs.write = %v, want ErrRoleAboveOwn", err)
	}
	if _, tok, err := tn.CreateMemberToken(ctx, "o@example.com", store.CLITokenScopes(store.RoleOwner), "owner", now); err != nil || slices.Contains(tok.Scopes, store.ScopeTeamAdmin) {
		t.Fatalf("an owner's member token = %+v, %v; want the editor's scopes", tok, err)
	}
	if _, _, err := tn.CreateMemberToken(ctx, "o@example.com", []string{store.ScopeTeamAdmin}, "owner", now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("an owner minting team.admin = %v, want ErrInvalidInput", err)
	}
	if _, _, err := tn.CreateMemberToken(ctx, "agent:o", []string{"runs.read"}, "owner", now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("a member token under an agent principal = %v, want ErrInvalidInput", err)
	}
	if _, _, err := tn.CreateMemberToken(ctx, "o@example.com", []string{"nodes.claim"}, "owner", now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("a member token with machine scopes = %v, want ErrInvalidInput", err)
	}
	if _, _, err := tn.CreateMemberToken(ctx, "x@example.com", []string{"runs.read"}, "stranger", now); !errors.Is(err, store.ErrNotMember) {
		t.Fatalf("a stranger minting = %v, want ErrNotMember", err)
	}
}

func TestMachineTokenNeedsAnEditor(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tn := defaultTenant(t, st)
	seedMinter(t, st, store.DefaultTeam, "reader", store.RoleReader)
	if _, _, err := tn.CreateMachineToken(ctx, "box", []string{"nodes.claim"}, "reader", time.Now()); !errors.Is(err, store.ErrRoleAboveOwn) {
		t.Fatalf("a reader minting a machine token = %v, want ErrRoleAboveOwn", err)
	}
	if _, _, err := tn.CreateMachineToken(ctx, "box", []string{"runs.read"}, "reader", time.Now()); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("a machine token with member scopes = %v, want ErrInvalidInput", err)
	}
}

// The per-member and per-team ceilings were set for CLI and runner tokens, and
// the tokens minted before the team kind still authenticate, so they count.
func TestTeamTokenLimitsCountLegacyTokens(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tn := defaultTenant(t, st)
	seedMinter(t, st, store.DefaultTeam, "ed", store.RoleEditor)
	now := time.Now().UTC()
	for range store.MaxCLITokensPerMember - 1 {
		if _, _, err := tn.CreateTokenWith(ctx, "ed@example.com", store.TokenKindUser, []string{"runs.read"},
			store.CLITokenLifetime, now, store.TokenOptions{CreatedBy: "ed"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := tn.CreateMemberToken(ctx, "ed@example.com", []string{"runs.read"}, "ed", now); err != nil {
		t.Fatalf("the last member slot = %v", err)
	}
	if _, _, err := tn.CreateMemberToken(ctx, "ed@example.com", []string{"runs.read"}, "ed", now); !errors.Is(err, store.ErrCLITokenLimit) {
		t.Fatalf("a member token past the limit = %v, want ErrCLITokenLimit", err)
	}
	if _, _, err := tn.CreateMachineToken(ctx, "box", []string{"nodes.claim"}, "ed", now); err != nil {
		t.Fatalf("a machine token beside a full member quota = %v", err)
	}
	for i := range store.MaxRunnerTokensPerTeam - 1 {
		if _, _, err := tn.CreateTokenWith(ctx, "agent:legacy-"+string(rune('a'+i%26))+strings.Repeat("x", i/26),
			store.TokenKindRunner, []string{"nodes.claim"}, store.RunnerTokenLifetime, now, store.TokenOptions{CreatedBy: "ed"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := tn.CreateMachineToken(ctx, "one-more", []string{"nodes.claim"}, "ed", now); !errors.Is(err, store.ErrRunnerTokenLimit) {
		t.Fatalf("a machine token past the limit = %v, want ErrRunnerTokenLimit", err)
	}
}

func TestAgentNameIsUniqueAcrossLegacyRunnerAndMachineTokens(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tn := defaultTenant(t, st)
	seedMinter(t, st, store.DefaultTeam, "ed", store.RoleEditor)
	now := time.Now().UTC()
	_, legacy, err := tn.CreateTokenWith(ctx, "agent:box", store.TokenKindRunner, []string{"nodes.claim"},
		store.RunnerTokenLifetime, now, store.TokenOptions{CreatedBy: "ed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tn.CreateMachineToken(ctx, "box", []string{"nodes.claim"}, "ed", now); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a machine token over a legacy runner token's name = %v, want ErrAgentNameTaken", err)
	}
	if err := tn.RevokeTeamToken(ctx, legacy.Prefix, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tn.CreateMachineToken(ctx, "box", []string{"nodes.claim"}, "ed", now); err != nil {
		t.Fatalf("a machine token after the legacy one was revoked = %v", err)
	}
	if _, _, err := tn.CreateTokenWith(ctx, "agent:box", store.TokenKindRunner, []string{"nodes.claim"},
		0, now, store.TokenOptions{}); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a runner token over a machine token's name = %v, want ErrAgentNameTaken", err)
	}
	// safety: the read in refuseTakenAgentName passes for a row written past it, so the index must refuse too.
	if _, err := st.DB().Exec(storetest.Rebind(st, `INSERT INTO tokens (team, hash, prefix, principal, kind, scopes, created_at)
		VALUES (?, 'h', 'swr_racer00', 'agent:box', 'runner', 'nodes.claim', ?)`), string(store.DefaultTeam), now.Unix()); err == nil {
		t.Fatal("the agent-name index admitted a runner row beside a live machine team token")
	}
}

func TestLegacyRunnerAndMachineTokensSlideOnUse(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tn := defaultTenant(t, st)
	seedMinter(t, st, store.DefaultTeam, "ed", store.RoleEditor)
	minted := time.Now().UTC().Truncate(time.Second)
	legacy, _, err := tn.CreateTokenWith(ctx, "agent:old", store.TokenKindRunner, []string{"nodes.claim"},
		store.RunnerTokenLifetime, minted, store.TokenOptions{CreatedBy: "ed"})
	if err != nil {
		t.Fatal(err)
	}
	machine, _, err := tn.CreateMachineToken(ctx, "new", []string{"nodes.claim"}, "ed", minted)
	if err != nil {
		t.Fatal(err)
	}
	member, _, err := tn.CreateMemberToken(ctx, "ed@example.com", []string{"runs.read"}, "ed", minted)
	if err != nil {
		t.Fatal(err)
	}
	used := minted.Add(store.RunnerTokenLifetime - time.Hour)
	for raw, slides := range map[string]bool{legacy: true, machine: true, member: false} {
		tok, err := st.LookupToken(raw, used)
		if err != nil {
			t.Fatalf("%s inside its lifetime: %v", raw[:store.PrefixLen], err)
		}
		if got := tok.ExpiresAt.Equal(used.Add(store.RunnerTokenLifetime)); got != slides {
			t.Errorf("%s expiry after a use = %v; slides %v, want %v", raw[:store.PrefixLen], tok.ExpiresAt, got, slides)
		}
	}
}

// A member lists what they minted, legacy CLI and runner tokens included, and
// an editor also sees every machine the team runs; a demotion sweeps both kinds.
func TestTeamTokensListsOwnTokensAndTheTeamsMachines(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tn := defaultTenant(t, st)
	seedMinter(t, st, store.DefaultTeam, "owner", store.RoleOwner)
	seedMinter(t, st, store.DefaultTeam, "ed", store.RoleEditor)
	seedMinter(t, st, store.DefaultTeam, "other", store.RoleEditor)
	now := time.Now().UTC()
	mint := func(principal, kind, by string, scopes ...string) string {
		t.Helper()
		_, tok, err := tn.CreateTokenWith(ctx, principal, kind, scopes, time.Hour, now, store.TokenOptions{CreatedBy: by})
		if err != nil {
			t.Fatal(err)
		}
		return tok.Prefix
	}
	legacyCLI := mint("ed@example.com", store.TokenKindUser, "ed", "runs.read")
	legacyRunner := mint("agent:old", store.TokenKindRunner, "ed", "nodes.claim")
	_, member, err := tn.CreateMemberToken(ctx, "ed@example.com", []string{"runs.read"}, "ed", now)
	if err != nil {
		t.Fatal(err)
	}
	_, machine, err := tn.CreateMachineToken(ctx, "new", []string{"nodes.claim"}, "ed", now)
	if err != nil {
		t.Fatal(err)
	}
	_, othersMember, err := tn.CreateMemberToken(ctx, "other@example.com", []string{"runs.read"}, "other", now)
	if err != nil {
		t.Fatal(err)
	}
	operator := mint("ops", store.TokenKindUser, "", "runs.read")

	prefixes := func(accountID string, machines bool) []string {
		t.Helper()
		toks, err := tn.TeamTokens(ctx, accountID, machines, now)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tok := range toks {
			out = append(out, tok.Prefix)
		}
		slices.Sort(out)
		return out
	}
	sorted := func(p ...string) []string { slices.Sort(p); return p }
	if got, want := prefixes("ed", false), sorted(legacyCLI, legacyRunner, member.Prefix, machine.Prefix); !slices.Equal(got, want) {
		t.Errorf("ed's own tokens = %v, want %v", got, want)
	}
	if got, want := prefixes("other", true), sorted(legacyRunner, machine.Prefix, othersMember.Prefix); !slices.Equal(got, want) {
		t.Errorf("another editor's view = %v, want %v", got, want)
	}
	if got := prefixes("owner", true); slices.Contains(got, operator) || slices.Contains(got, legacyCLI) {
		t.Errorf("an owner's view %v holds an operator token or another member's CLI token", got)
	}
	revoked, err := tn.SetMemberRole(ctx, "owner", "ed", store.RoleReader, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sorted(revoked...), sorted(legacyCLI, legacyRunner, member.Prefix, machine.Prefix); !slices.Equal(got, want) {
		t.Errorf("demotion to reader revoked %v, want %v", got, want)
	}
}

func TestTeamTokenReadsOnlyTokensATeamMints(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tn := defaultTenant(t, st)
	_, svc, err := tn.CreateToken(ctx, "local-run:x", store.TokenKindService, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tn.TeamToken(ctx, svc.Prefix); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TeamToken on a service token = %v, want ErrNotFound", err)
	}
	if err := tn.RevokeTeamToken(ctx, svc.Prefix, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RevokeTeamToken on a service token = %v, want ErrNotFound", err)
	}
}
