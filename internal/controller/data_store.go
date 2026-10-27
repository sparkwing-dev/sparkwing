package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type dataStoreAccess struct {
	resolveTenant func(context.Context, store.Team) (TenantHandle, error)
}

func (d dataStoreAccess) CheckDataAccess(ctx context.Context, authorized capability.Authorized, target capability.DataTarget) error {
	tenant, err := scopedTenantPort[capability.DataAccessChecker](ctx, store.Team(authorized.Principal.Capability.Team), d.resolveTenant)
	if err != nil {
		return err
	}
	return tenant.CheckDataAccess(ctx, authorized, target)
}

func (d *DataController) bind(authority *store.Operator, resolve func(context.Context, store.Team) (TenantHandle, error)) {
	if d.authority == nil && authority != nil {
		d.authority = authority
	}
	if d.ResolveTenant == nil {
		d.ResolveTenant = resolve
	}
	if d.Access == nil && d.ResolveTenant != nil {
		d.Access = dataStoreAccess{d.ResolveTenant}
	}
}

func (s *Server) configureDataStore(d *DataController) {
	d.bind(s.operator, s.resolveTenant)
	if d.authority == nil {
		return
	}
	if d.ReadBlob == nil {
		d.ReadBlob = func(ctx context.Context, authority blob.Authority, digest blob.Digest) (blob.Blob, error) {
			tenant, err := d.authorityTenant(ctx, authority)
			if err != nil {
				return blob.Blob{}, err
			}
			return tenant.ReadDataBlob(ctx, authority, digest)
		}
	}
	if d.ChargeDownload == nil {
		d.ChargeDownload = func(ctx context.Context, authority blob.Authority, metadata blob.Blob) error {
			tenant, err := d.authorityTenant(ctx, authority)
			if err != nil {
				return err
			}
			_, err = tenant.ChargeDataDownload(ctx, authority, metadata, store.DownloadCharge{Bytes: metadata.Size, FreeCapBytes: s.downloadFree, FundedCapBytes: s.downloadFunded})
			return err
		}
	}
}

func (d *DataController) authorityTenant(ctx context.Context, authority blob.Authority) (*store.Tenant, error) {
	if d.authority == nil {
		return nil, routepolicy.MissingEndpointPolicy()
	}
	p, err := d.authority.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: authority.CapabilityRef})
	if err != nil {
		return nil, err
	}
	return scopedTenantPort[*store.Tenant](ctx, store.Team(p.Capability.Team), d.ResolveTenant)
}
