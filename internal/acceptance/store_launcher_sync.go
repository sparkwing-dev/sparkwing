package acceptance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckStoreLauncherSync verifies award reconstruction and cleanup fences through reopening.
func CheckStoreLauncherSync(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "launcher.db")
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE nodes SET claimed_by=NULL,capability_prefix='',generation=0,lease_expires_at=NULL`); err != nil {
		return err
	}
	until := time.Now().UTC().Add(time.Hour)
	launcher := capability.Capability{Digest: sha256.Sum256([]byte("launcher-root")), Prefix: "launcher-root", Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Audience: capability.Controller, Grants: []capability.Grant{capability.ClaimsLaunch}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, IssuedAt: time.Now().UTC(), ExpiresAt: &until, ReservedPools: []string{}, Repositories: []string{}}
	if err := seedRecoveryCapability(ctx, f.s, launcher); err != nil {
		return err
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: launcher.Prefix}
	a := capability.Authorized{Principal: capability.Principal{Authority: ref}}
	demand := store.UnitDemand{CapabilityPrefix: launcher.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, Available: match.Resources{Cores: 8}}
	first, err := f.s.ClaimUnit(ctx, demand)
	if err != nil {
		return fmt.Errorf("initial launcher claim: %w", err)
	}
	p, err := f.s.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: first.Award.CapabilityPrefix})
	if err != nil {
		return err
	}
	old := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: p.Capability.UnitID, Holder: first.Award.Holder, Generation: first.Award.Generation}, Job: controllerwire.LauncherJobRef{Namespace: "ci", Name: "first", UID: "first-uid"}, NodeUID: "node-uid"}
	for exchange := range 2 {
		if err := f.reopen(ctx, path); err != nil {
			return err
		}
		response, err := (&controller.EngineController{Store: f.s}).SyncLauncher(ctx, a, controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{old}})
		if err != nil {
			return err
		}
		if len(response.Awards) != 1 || len(response.Remove) != 0 || response.Awards[0].Capability != "" || response.Awards[0].UnitID != old.Award.ID || response.Awards[0].Binding.Generation != first.Award.Generation || response.Awards[0].Delivery != capability.DeliveryBootstrap {
			return errors.New("reopening or exchange lost the immutable launcher award")
		}
		if exchange == 0 {
			issued, err := f.s.ExchangeCapability(ctx, capability.ExchangeSpec{BootstrapPrefix: first.Award.CapabilityPrefix})
			if err != nil {
				return err
			}
			first.Award.CapabilityPrefix = issued.Capability.Prefix
		}
	}
	if err := f.s.ReleaseUnit(ctx, first, first.Award.CapabilityPrefix); err != nil {
		return fmt.Errorf("release exchanged claim: %w", err)
	}
	if _, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Actor: f.owner, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
		return fmt.Errorf("owner redispatch: %w", err)
	}
	second, err := f.s.ClaimUnit(ctx, demand)
	if err != nil {
		return fmt.Errorf("claim replacement: %w", err)
	}
	current := old
	current.Award.Generation = second.Award.Generation
	current.Job = controllerwire.LauncherJobRef{Namespace: "ci", Name: "second", UID: "second-uid"}
	if err := f.reopen(ctx, path); err != nil {
		return err
	}
	response, err := (&controller.EngineController{Store: f.s}).SyncLauncher(ctx, a, controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{old, current}})
	if err != nil {
		return err
	}
	if len(response.Awards) != 1 || response.Awards[0].Capability != "" || response.Awards[0].Binding.Generation != second.Award.Generation || len(response.Remove) != 1 || response.Remove[0] != old.Job {
		return errors.New("stale Job cleanup selected the replacement award")
	}
	row, err := f.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		return err
	}
	if row.Generation != second.Award.Generation || row.Attempt != 0 || row.CapabilityPrefix != second.Award.CapabilityPrefix {
		return errors.New("launcher synchronization changed execution state")
	}
	return nil
}
