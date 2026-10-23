package controller

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) sourceUploadEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "POST", Pattern: "/api/v1/data/source", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleSourceUpload},
		{Method: "POST", Pattern: "/api/v1/data/source/{upload}/commit", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleSourceCommit},
	}
}

func (s *Server) sourceUploadTenant(r *http.Request, a capability.Authorized) (*store.Tenant, error) {
	if s.dataController == nil || s.dataController.Objects == nil || s.operator == nil {
		return nil, &apierr.Error{Code: apierr.Unavailable, Message: "source upload requires shared object storage"}
	}
	return s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
}

func (s *Server) handleSourceUpload(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request controllerwire.SourceUploadRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	input := runspec.Request{Git: request.Git, Source: request.Source}
	if err := s.resolveIntakeSource(r.Context(), a.Target.Team, &input); err != nil {
		return err
	}
	tenant, err := s.sourceUploadTenant(r, a)
	if err != nil {
		return err
	}
	u, err := tenant.ReserveSourceUpload(r.Context(), a.Principal.Authority, store.SourceUpload{ID: rand.Text(), Scope: store.SourceUploadScope{Repository: input.Git.Repository.Key, RepositoryDisplay: input.Git.Repository.Display, ExecutionSHA: input.Git.SHA, PipelineSHA: input.Source.Ref}, Blob: blob.Blob{Team: a.Target.Team, Digest: request.Digest, Size: request.Size}, ExpiresAt: time.Now().UTC().Add(limits.DataUploadURLTTL)})
	if err != nil {
		return dataRouteError(err)
	}
	object := capability.ObjectToSign{Method: http.MethodPut, Key: sourceStagingKey(a.Target.Team, u.ID), Size: u.Blob.Size, SHA256: string(u.Blob.Digest), ExpiresAt: u.ExpiresAt}
	signed, err := s.dataController.Objects.Sign(r.Context(), object)
	if err == nil && !signedDataMatches(signed, object) {
		err = dataProjectionError()
	}
	if err != nil {
		cleanup := context.WithoutCancel(r.Context())
		return errors.Join(dataRouteError(err), tenant.AbortSourceUpload(cleanup, a.Principal.Authority, u.ID), s.dataController.Objects.DeleteObject(cleanup, object.Key))
	}
	writeJSON(w, http.StatusOK, controllerwire.DataUploadTicket{UploadID: u.ID, URL: signed.URL, Headers: signed.Headers, ExpiresAt: signed.ExpiresAt, SHA256: u.Blob.Digest, Size: u.Blob.Size})
	return nil
}

func sourceStagingKey(team, id string) string { return "teams/" + team + "/pending/" + id }

func (s *Server) handleSourceCommit(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body struct{}
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	tenant, err := s.sourceUploadTenant(r, a)
	if err != nil {
		return err
	}
	u, err := tenant.ReadSourceUpload(r.Context(), a.Principal.Authority, r.PathValue("upload"))
	if err != nil {
		return dataRouteError(err)
	}
	if u.CommittedAt == 0 {
		if err := s.dataController.Objects.CommitObject(r.Context(), sourceStagingKey(a.Target.Team, u.ID), u.Blob); err != nil {
			return dataRouteError(err)
		}
	}
	u, err = tenant.CommitSourceUpload(r.Context(), a.Principal.Authority, u.ID, u.Blob, s.dataController.replicaID)
	if err != nil {
		return dataRouteError(err)
	}
	if err := s.dataController.Objects.DeleteObject(context.WithoutCancel(r.Context()), sourceStagingKey(a.Target.Team, u.ID)); err != nil {
		return dataRouteError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.DataReceipt{Digest: u.Blob.Digest, CreatedAt: time.Unix(0, u.CommittedAt).UTC()})
	return nil
}
