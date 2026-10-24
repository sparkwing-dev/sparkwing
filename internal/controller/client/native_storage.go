package client

import (
	"context"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

// NativeStorage joins the authenticated byte and record transports with their canonical scope.
type NativeStorage struct {
	Bytes     blob.Store
	Records   blob.RecordStore
	Authority blob.Authority
}

func (c *LocalHostClient) nativeTransport(ctx context.Context, principal capability.Principal) (*engineClient, error) {
	if err := c.live(); err != nil {
		return nil, err
	}
	owner, err := c.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if principal.Authority == owner.Authority && principal.Capability.Team == owner.Capability.Team && principal.Capability.Binding.RunID == c.run {
		return c.transport, nil
	}
	if principal.Capability.Unit != capability.UnitNode || principal.Capability.Team != owner.Capability.Team {
		return nil, localHostClientRefusal()
	}
	transport, award, err := c.nodeTransport(ctx, runspec.NodeRef{RunID: runspec.RunID(c.run), NodeID: runspec.NodeID(principal.Capability.Binding.NodeID)})
	if err != nil {
		return nil, err
	}
	if principal.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: award.prefix}) || principal.Capability.Binding.Generation != award.generation {
		return nil, localHostClientRefusal()
	}
	return transport, nil
}

// BindStorage constructs shared byte and record clients under the current awarded authority.
func (c *LocalHostClient) BindStorage(ctx context.Context, principal capability.Principal) (NativeStorage, error) {
	transport, err := c.nativeTransport(ctx, principal)
	if err != nil {
		return NativeStorage{}, err
	}
	pattern, path := "/api/v1/runs/{run}/local-host", "/api/v1/runs/"+url.PathEscape(c.run)+"/local-host"
	if principal.Capability.Unit == capability.UnitNode {
		pattern, path = pattern+"/nodes/{job}", path+"/nodes/"+url.PathEscape(principal.Capability.Binding.NodeID)
	}
	var response localhostwire.LocalDataAuthority
	if err := transport.request(ctx, "GET", pattern+"/data-authority", path+"/data-authority", "", nil, &response); err != nil {
		return NativeStorage{}, err
	}
	authority := response.Authority
	if authority.CapabilityRef != principal.Capability.Prefix || authority.Principal != blob.Principal(principal.Capability.Subject.ID) || principal.Capability.Data == nil || string(authority.Repo) != principal.Capability.Data.RepositoryKey {
		return NativeStorage{}, localHostClientRefusal()
	}
	data := &dataClient{transport: transport, authority: &authority}
	return NativeStorage{Bytes: blob.NewSignedStore(blob.SignedStoreOptions{Signer: data, HTTPClient: blob.TransferClient{Transport: transport.http.Transport}}), Records: data, Authority: authority}, nil
}
