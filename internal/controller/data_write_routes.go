package controller

import (
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type dataUploadRequest struct {
	Digest blob.Digest `json:"digest"`
	Size   *int64      `json:"size"`
}

type dataCommitRequest struct {
	UploadID string `json:"upload_id"`
}

type dataUploadResponse struct {
	UploadID  string            `json:"upload_id"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
	SHA256    blob.Digest       `json:"sha256"`
	Size      int64             `json:"size"`
}

type dataReceiptResponse struct {
	Digest    blob.Digest `json:"digest"`
	CreatedAt time.Time   `json:"created_at"`
}

func (s *Server) handleDataUpload(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request dataUploadRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if !validDataDigest(request.Digest) || request.Size == nil || *request.Size < 0 {
		return invalidEngineBody("upload requires a SHA-256 digest and nonnegative size")
	}
	binding, err := s.dataController.BindData(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	ticket, err := binding.Signer.Upload(r.Context(), blob.UploadIntent{Authority: binding.Authority, Identity: binding.Authority.Upload.Value, Digest: request.Digest, Size: *request.Size})
	if err != nil {
		return dataRouteError(err)
	}
	transfer := ticket.Transfer
	writeJSON(w, http.StatusOK, dataUploadResponse{UploadID: ticket.ID, URL: transfer.URL, Headers: transfer.Headers, ExpiresAt: transfer.ExpiresAt, SHA256: transfer.Digest, Size: transfer.Size})
	return nil
}

func (s *Server) handleDataCommit(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request dataCommitRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if request.UploadID == "" || strings.ContainsAny(request.UploadID, "/\\\x00") {
		return invalidEngineBody("commit requires an upload ticket")
	}
	binding, err := s.dataController.BindData(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	receipt, err := binding.Signer.Commit(r.Context(), blob.CommitUpload{Authority: binding.Authority, TicketID: request.UploadID})
	if err != nil {
		return dataRouteError(err)
	}
	writeJSON(w, http.StatusOK, dataReceiptResponse{Digest: receipt.Digest, CreatedAt: receipt.CreatedAt})
	return nil
}
