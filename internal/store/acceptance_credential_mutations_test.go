package store_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func checkStoreCredentialMutations(ctx context.Context, s *store.Store, team store.Team, owner runstate.Actor) error {
	tenant, err := s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	p, err := s.AsOperator().ResolveAuthority(ctx, owner.Authority)
	if err != nil {
		return err
	}
	a := capability.Authorized{Principal: p, Target: capability.Target{Team: string(team), SecretName: "CONFIG", Repo: "gh:1", Pipeline: "build"}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		return err
	}
	sealed, err := store.SealSecret(cipher, team, store.Secret{Name: "CONFIG", Value: "stored-envelope", Repo: "gh:1", Pipeline: "build"})
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
	subID := store.RunEventSubscriptionID(team, "acceptance")
	sealedSub, err := store.SealRunEventSubscription(cipher, team, store.RunEventSubscription{ID: subID, URL: "https://hooks.example.test/a", Secret: "whsec_acceptance", Filter: runevents.Filter{EventTypes: []string{runevents.EventFinished}, Statuses: []string{"failed"}}})
	if err != nil {
		return err
	}
	sub, replayed, err := tenant.CreateRunEventSubscription(ctx, a, sealedSub)
	if err != nil || replayed || sub.CreatedPrincipal != p.Capability.Subject.ID {
		return fmt.Errorf("current session subscription transaction: %+v, %v, %w", sub, replayed, err)
	}
	if _, replayed, err := tenant.CreateRunEventSubscription(ctx, a, sealedSub); err != nil || !replayed {
		return fmt.Errorf("subscription replay: %v, %w", replayed, err)
	}
	subs, err := tenant.RunEventSubscriptions(ctx)
	if err != nil || len(subs) != 1 || subs[0].Filter.Statuses[0] != "failed" || len(subs[0].Filter.Pipelines) != 0 {
		return fmt.Errorf("subscription list: %+v, %w", subs, err)
	}
	rotated, err := store.SealRunEventSubscription(cipher, team, store.RunEventSubscription{ID: subID, Secret: "whsec_rotated"})
	if err != nil {
		return err
	}
	if row, err := tenant.RotateRunEventSubscriptionSecret(ctx, a, rotated); err != nil || row.Secret != rotated.Row().Secret {
		return fmt.Errorf("subscription rotation: %w", err)
	}
	if deleted, err := tenant.DeleteRunEventSubscription(ctx, a, subID); err != nil || !deleted {
		return fmt.Errorf("subscription deletion: %v, %w", deleted, err)
	}
	if err := tenant.RemoveSecret(ctx, a, "CONFIG", "gh:1", "build"); err != nil {
		return err
	}
	if err := tenant.RemoveGitCredential(ctx, a, git.Host, ""); err != nil {
		return err
	}
	if err := tenant.RemoveSecret(ctx, a, "CONFIG", "gh:1", "build"); !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("missing secret deletion: %v", err)
	}
	if err := tenant.RemoveGitCredential(ctx, a, git.Host, ""); !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("missing git deletion: %v", err)
	}
	return nil
}
