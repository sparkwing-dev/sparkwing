package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type dataDownloadRequest struct {
	Digest blob.Digest `json:"digest"`
}

type dataPresenceRequest struct {
	Digests  []blob.Digest `json:"digests"`
	Deadline time.Time     `json:"deadline,omitzero"`
}

type dataPresenceResponse struct {
	Present []blob.Digest `json:"present"`
}

// WithDataController supplies authenticated byte and record adapters.
func (s *Server) WithDataController(data *DataController) *Server {
	s.dataController = data
	return s
}

func (s *Server) dataEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/data/upload", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDataUpload},
		{Method: "POST", Pattern: "/api/v1/data/commit", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDataCommit},
		{Method: "POST", Pattern: "/api/v1/data/records", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDataRecordCommit},
		{Method: "GET", Pattern: "/api/v1/data/records", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDataRecordLookup},
		{Method: "POST", Pattern: "/api/v1/data/download", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDataDownload},
		{Method: "POST", Pattern: "/api/v1/data/has-many", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDataPresence},
	}
}

func (s *Server) handleDataDownload(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request dataDownloadRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if !validDataDigest(request.Digest) {
		return invalidEngineBody("download requires a SHA-256 digest")
	}
	binding, err := s.dataController.BindData(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	transfer, err := binding.Signer.Download(r.Context(), blob.GetRequest{Authority: binding.Authority, Digest: request.Digest})
	if err != nil {
		return dataRouteError(err)
	}
	writeJSON(w, http.StatusOK, DataDownloadResponse{URL: transfer.URL, Headers: transfer.Headers, SHA256: string(transfer.Digest), Size: transfer.Size, Expires: transfer.ExpiresAt})
	return nil
}

func (s *Server) handleDataPresence(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request dataPresenceRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if request.Digests == nil {
		return invalidEngineBody("digests must be an array")
	}
	for _, digest := range request.Digests {
		if !validDataDigest(digest) {
			return invalidEngineBody("existence queries require SHA-256 digests")
		}
	}
	binding, err := s.dataController.BindData(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	presence, err := binding.Signer.HasMany(r.Context(), blob.HasManyRequest{Authority: binding.Authority, Digests: request.Digests, Budget: blob.Budget{Deadline: request.Deadline}})
	if err != nil {
		return dataRouteError(err)
	}
	writeJSON(w, http.StatusOK, dataPresenceResponse{Present: presence.Present})
	return nil
}

func dataRouteError(err error) error {
	var quota *store.DownloadCapError
	switch {
	case errors.Is(err, blob.ErrDigestNotReadable):
		return &apierr.Error{Code: apierr.Forbidden, Message: "digest is not readable", Details: map[string]any{"reason": apierr.ReasonDigestNotReadable}}
	case errors.Is(err, blob.ErrIntegrity):
		return &apierr.Error{Code: apierr.IntegrityFailure, Message: "stored bytes do not match their declared digest or size"}
	case errors.Is(err, store.ErrStorageQuota):
		return &apierr.Error{Code: apierr.QuotaExceeded, Message: "storage quota exceeded"}
	case errors.Is(err, blob.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return &apierr.Error{Code: apierr.BlobMissing, Message: "blob is absent or tombstoned"}
	case errors.Is(err, blob.ErrBudget), errors.Is(err, context.DeadlineExceeded):
		return &apierr.Error{Code: apierr.DeadlineExceeded, Message: "data query deadline exceeded"}
	case errors.As(err, &quota):
		return &apierr.Error{Code: apierr.RateLimited, Message: "daily download cap reached", Details: map[string]any{"retry_after_ms": quota.RetryAfter.Milliseconds()}}
	default:
		return engineLookupError(err)
	}
}
