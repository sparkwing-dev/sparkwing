package store_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func checkStoreCredentialMutations(ctx context.Context, s *store.Store, team store.Team, owner store.Actor) error {
	tenant, err := s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	p, err := s.AsOperator().ResolveAuthority(ctx, owner.Authority)
	if err != nil {
		return err
	}
	a := capability.Authorized{Principal: p, Target: capability.Target{Team: string(team), SecretName: "CONFIG", Pipeline: "build"}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		return err
	}
	sealed, err := store.SealSecret(cipher, team, store.Secret{Name: "CONFIG", Value: "stored-envelope", Pipeline: "build"})
	if err != nil {
		return err
	}
	if _, err := tenant.WriteSecret(ctx, a, sealed); err != nil {
		return err
	}
	row, err := tenant.ReadSecret(ctx, a)
	if err != nil || row == nil || row.Value != sealed.Row().Value || row.Principal != p.Capability.Subject.ID || row.CreatedAt.IsZero() {
		return fmt.Errorf("current session secret transaction: %+v, %v", row, err)
	}
	sealedGit, err := store.SealGitCredential(cipher, team, store.GitCredential{HostWide: true, Host: "git.example.test", Kind: store.GitCredentialHTTPS, Secret: "stored-envelope"})
	if err != nil {
		return err
	}
	git, err := tenant.WriteGitCredential(ctx, a, sealedGit)
	if err != nil || git.Team != team || git.CreatedBy != p.Capability.Subject.ID || git.ConfirmedAt == nil {
		return fmt.Errorf("current session git transaction: %+v, %v", git, err)
	}
	if err := tenant.RemoveSecret(ctx, a, "CONFIG", "build"); err != nil {
		return err
	}
	if err := tenant.RemoveGitCredential(ctx, a, git.Host, ""); err != nil {
		return err
	}
	if err := tenant.RemoveSecret(ctx, a, "CONFIG", "build"); !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("missing secret deletion: %v", err)
	}
	if err := tenant.RemoveGitCredential(ctx, a, git.Host, ""); !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("missing git deletion: %v", err)
	}
	return nil
}
