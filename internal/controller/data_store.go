package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type dataStoreAccess struct{ store *store.Store }

func (d dataStoreAccess) CheckDataAccess(ctx context.Context, authorized capability.Authorized, target capability.DataTarget) error {
	tenant, err := d.store.ForTeam(ctx, store.Team(authorized.Principal.Capability.Team))
	if err != nil {
		return err
	}
	return tenant.CheckDataAccess(ctx, authorized, target)
}

func (s *Server) configureDataStore(d *DataController) {
	if d.Store == nil {
		return
	}
	d.replicaID = s.leaseHolder
	if d.Access == nil {
		d.Access = dataStoreAccess{d.Store}
	}
	if d.ResolveAuthority == nil {
		d.ResolveAuthority = func(ctx context.Context, authorized capability.Authorized) (blob.Authority, error) {
			tenant, err := d.Store.ForTeam(ctx, store.Team(authorized.Target.Team))
			if err != nil {
				return blob.Authority{}, err
			}
			return tenant.ResolveDataAuthority(ctx, authorized)
		}
	}
	if d.ReadBlob == nil {
		d.ReadBlob = func(ctx context.Context, authority blob.Authority, digest blob.Digest) (blob.Blob, error) {
			tenant, err := dataAuthorityTenant(ctx, d.Store, authority)
			if err != nil {
				return blob.Blob{}, err
			}
			return tenant.ReadDataBlob(ctx, authority, digest)
		}
	}
	if d.ChargeDownload == nil {
		d.ChargeDownload = func(ctx context.Context, authority blob.Authority, metadata blob.Blob) error {
			tenant, err := dataAuthorityTenant(ctx, d.Store, authority)
			if err != nil {
				return err
			}
			_, err = tenant.ChargeDataDownload(ctx, authority, metadata, store.DownloadCharge{ReplicaID: s.leaseHolder, Bytes: metadata.Size, FreeCapBytes: s.downloadFree, FundedCapBytes: s.downloadFunded})
			return err
		}
	}
}

func dataAuthorityTenant(ctx context.Context, st *store.Store, authority blob.Authority) (*store.Tenant, error) {
	p, err := st.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: authority.CapabilityRef})
	if err != nil {
		return nil, err
	}
	return st.ForTeam(ctx, store.Team(p.Capability.Team))
}

var _ capability.DataAccessChecker = dataStoreAccess{}
