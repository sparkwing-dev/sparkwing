package client

import (
	"context"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
)

// ProfileData holds a revocable account data opt-in and its private transport.
type ProfileData struct {
	Options   EngineOptions
	Authority blob.Authority
	Team      string
	revoke    func(context.Context) error
}

// Close revokes the opt-in through its original profile authority.
func (p ProfileData) Close(ctx context.Context) error {
	if p.revoke == nil {
		return nil
	}
	return p.revoke(ctx)
}

// OpenProfileData narrows a live profile to verified cache reads or one publication.
func OpenProfileData(ctx context.Context, options EngineOptions, scope capability.DataScope) (ProfileData, error) {
	if scope.Use != capability.DataRemoteCache && scope.Use != capability.DataPublish || scope.Use == capability.DataPublish && blob.ParseCapabilityScope(scope.WriteRef).Verified {
		return ProfileData{}, dataProtocolError("invalid profile data purpose or ref")
	}
	c, err := newEngineClient(options)
	if err != nil {
		return ProfileData{}, err
	}
	grant := capability.DataRead
	if scope.Use == capability.DataPublish {
		grant = capability.DataWrite
	}
	var result controllerwire.CreatedToken
	err = c.request(ctx, "POST", "/api/v1/tokens", "/api/v1/tokens", "", controllerwire.CreateTokenRequest{Grants: []string{string(grant)}, Data: &scope}, &result)
	if err != nil {
		return ProfileData{}, err
	}
	if result.Capability == "" || result.Prefix == "" || result.Team == "" || result.Principal == "" {
		return ProfileData{}, dataProtocolError("incomplete profile data credential")
	}
	p := ProfileData{Options: options, Team: result.Team, Authority: blob.Authority{CapabilityRef: result.Prefix, Principal: blob.Principal(result.Principal), Repo: runspec.RepoKey(scope.RepositoryKey)}}
	p.Options.HTTP = c.http
	p.Options.Bearer = func(ctx context.Context) (string, error) { return result.Capability, ctx.Err() }
	p.revoke = func(ctx context.Context) error {
		return c.request(ctx, "DELETE", "/api/v1/tokens/{token}", "/api/v1/tokens/"+url.PathEscape(result.Prefix), "", nil, nil)
	}
	if scope.Use == capability.DataPublish {
		p.Authority.Ref.Scope = blob.ParseCapabilityScope(scope.WriteRef)
		p.Authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: blob.UploadIdentity{Kind: blob.UploadPublication, Team: result.Team, RunID: scope.RunID, Principal: p.Authority.Principal, CapabilityRef: result.Prefix}}
	}
	return p, nil
}

// publicationStorage binds signed writes to one imported local attempt.
func (p ProfileData) publicationStorage(attempt runspec.AttemptIdentity) (blob.Store, blob.RecordStore, blob.Authority, error) {
	a := p.Authority
	if !a.Upload.Set || a.Upload.Value.Kind != blob.UploadPublication || attempt.Team != p.Team || attempt.RunID != a.Upload.Value.RunID || attempt.NodeID == "" || attempt.Attempt <= 0 || attempt.Generation <= 0 {
		return nil, nil, a, localHostClientRefusal()
	}
	a.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}
	a.Upload.Value.Attempt = a.Attempt
	c, err := newEngineClient(p.Options)
	if err != nil {
		return nil, nil, a, err
	}
	data := &dataClient{transport: c, authority: &a}
	return blob.NewSignedStore(blob.SignedStoreOptions{Signer: data, HTTPClient: blob.TransferClient{Transport: c.http.Transport}}), data, a, nil
}

// publicationLogs uploads an imported attempt through the shared log segment path.
func (p ProfileData) publicationLogs(attempt runspec.AttemptIdentity) (logseg.Uploader, error) {
	if _, _, _, err := p.publicationStorage(attempt); err != nil {
		return nil, err
	}
	c, err := newEngineClient(p.Options)
	if err != nil {
		return nil, err
	}
	return &nativeLogUploader{transport: c, subject: blob.LogSubject{Kind: blob.LogJobAttempt, Team: attempt.Team, RunID: attempt.RunID, Attempt: &attempt}, pattern: "/api/v1/runs/{run}/jobs/{job}", path: "/api/v1/runs/" + url.PathEscape(attempt.RunID) + "/jobs/" + url.PathEscape(attempt.NodeID)}, nil
}

// RemoteCache binds this profile's read opt-in to the signed object and record clients.
func (p ProfileData) RemoteCache(profile string) (blob.RemoteCache, error) {
	c, err := newEngineClient(p.Options)
	if err != nil {
		return nil, err
	}
	data := &dataClient{transport: c, authority: &p.Authority}
	bytes := blob.NewSignedStore(blob.SignedStoreOptions{Signer: data, HTTPClient: blob.TransferClient{Transport: c.http.Transport}})
	return blob.OpenRemoteCache(blob.RemoteCacheOptions{Profile: profile, Team: p.Team, Repo: p.Authority.Repo, Authority: p.Authority, Records: data}, bytes)
}
