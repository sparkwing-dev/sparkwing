package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func sourceGitCredentialFixture(t *testing.T) (*Tenant, capability.Authorized, capability.Authorized) {
	t.Helper()
	tenant, source, _ := sourceReleaseFixture(t)
	tenant.s.sourcePolicy = nil
	owner := authorityMember(t, tenant.s, "owner", RoleOwner)
	admin := capability.Authorized{Principal: capability.Principal{Authority: owner.Authority}, Target: capability.Target{Team: string(tenant.team)}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	return tenant, admin, source
}

func TestSourceGitCredentialConfirmationAndReplacement(t *testing.T) {
	tenant, admin, source := sourceGitCredentialFixture(t)
	ctx := t.Context()
	row := GitCredential{Host: "github.com", Kind: GitCredentialSSH, Secret: "sealed-key", KnownHosts: "github.com ssh-ed25519 AAAA", Fingerprint: "SHA256:one"}
	stored, err := tenant.WriteGitCredential(ctx, admin, sealedGitFixture(t, tenant, row))
	if err != nil || stored.ConfirmedAt != nil || stored.Team != tenant.team {
		t.Fatalf("unconfirmed credential: %+v, %v", stored, err)
	}
	if c, audit, err := tenant.ReleaseSourceCredential(ctx, source); err == nil || c.ID != "" || audit.ID != "" {
		t.Fatalf("unconfirmed release: %+v, %+v, %v", c, audit, err)
	}
	if _, err := tenant.ConfirmGitCredentialFor(ctx, admin, row.Host, "SHA256:other"); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("wrong fingerprint: %v", err)
	}
	confirmed, err := tenant.ConfirmGitCredentialFor(ctx, admin, row.Host, row.Fingerprint)
	if err != nil || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirmation: %+v, %v", confirmed, err)
	}
	at := tenant.s.now().Add(time.Nanosecond)
	tenant.s.clock = func() time.Time { return at }
	replayed, err := tenant.ConfirmGitCredentialFor(ctx, admin, row.Host, row.Fingerprint)
	if err != nil || !replayed.ConfirmedAt.Equal(*confirmed.ConfirmedAt) || !replayed.UpdatedAt.Equal(confirmed.UpdatedAt) {
		t.Fatalf("confirmation replay: %+v, %v", replayed, err)
	}
	for _, fingerprint := range []string{"SHA256:one", "SHA256:two"} {
		row.Fingerprint, row.Secret = fingerprint, "sealed-rotated"
		rotated, err := tenant.WriteGitCredential(ctx, admin, sealedGitFixture(t, tenant, row))
		if err != nil || !rotated.CreatedAt.Equal(stored.CreatedAt) || rotated.ID == stored.ID {
			t.Fatalf("replacement: %+v, %v", rotated, err)
		}
		c, audit, err := tenant.ReleaseSourceCredential(ctx, source)
		if fingerprint == "SHA256:one" {
			if err != nil || c.Secret != rotated.Secret || c.KnownHosts != row.KnownHosts || audit.CredentialID != rotated.ID || audit.CapabilityPrefix != source.Principal.Capability.Prefix || audit.RunID != "run" || audit.Team != string(tenant.team) || !audit.ReleasedAt.Equal(at) {
				t.Fatalf("confirmed release: %+v, %+v, %v", c, audit, err)
			}
		} else if err == nil || c.ID != "" || audit.ID != "" || rotated.ConfirmedAt != nil {
			t.Fatalf("changed host key released: %+v, %+v, %v", c, audit, err)
		}
	}
	rows, err := tenant.SourceReleaseAudits(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("committed releases: %+v, %v", rows, err)
	}
}

func TestSourceGitCredentialHTTPSDeletionAndTenant(t *testing.T) {
	tenant, admin, source := sourceGitCredentialFixture(t)
	ctx := t.Context()
	row, err := tenant.WriteGitCredential(ctx, admin, sealedGitFixture(t, tenant, GitCredential{Host: "github.com", Kind: GitCredentialHTTPS, Secret: "sealed-token"}))
	if err != nil || row.ConfirmedAt == nil {
		t.Fatalf("HTTPS confirmation: %+v, %v", row, err)
	}
	foreign := &Tenant{s: tenant.s, team: "b"}
	if c, audit, err := foreign.ReleaseSourceCredential(ctx, source); err == nil || c.ID != "" || audit.ID != "" {
		t.Fatalf("foreign release: %+v, %+v, %v", c, audit, err)
	}
	if err := foreign.RemoveGitCredential(ctx, admin, row.Host); err == nil {
		t.Fatal("foreign credential deleted")
	}
	if c, _, err := tenant.ReleaseSourceCredential(ctx, source); err != nil || c.ID != row.ID {
		t.Fatalf("HTTPS release: %+v, %v", c, err)
	}
	if err := tenant.RemoveGitCredential(ctx, admin, row.Host); err != nil {
		t.Fatal(err)
	}
	if c, audit, err := tenant.ReleaseSourceCredential(ctx, source); !errors.Is(err, ErrNotFound) || c.ID != "" || audit.ID != "" {
		t.Fatalf("deleted release: %+v, %+v, %v", c, audit, err)
	}
	rows, err := tenant.SourceReleaseAudits(ctx, 10)
	if err != nil || len(rows) != 1 || rows[0].CredentialID != row.ID {
		t.Fatalf("audit after deletion: %+v, %v", rows, err)
	}
	rows, err = foreign.SourceReleaseAudits(ctx, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("foreign audits: %+v, %v", rows, err)
	}
}
