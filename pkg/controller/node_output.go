package controller

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type outputStore interface {
	grant(ctx context.Context, u store.Upload) (string, map[string]string, error)
	commit(ctx context.Context, u store.Upload) error
	resolve(r *http.Request, obj store.Upload) (string, time.Time, error)
}

var errOutputsUnavailable = errors.New("this controller has no output store")

func (s *Server) outputs() outputStore {
	if s.directUploads != nil {
		return s3Outputs{s: s}
	}
	return nil
}

type s3Outputs struct{ s *Server }

func (o s3Outputs) grant(ctx context.Context, u store.Upload) (string, map[string]string, error) {
	raw, err := hex.DecodeString(u.SHA256)
	if err != nil {
		return "", nil, err
	}
	return o.s.directUploads.presignPut(ctx, u, raw)
}

func (o s3Outputs) commit(ctx context.Context, u store.Upload) error {
	return o.s.directUploads.commit(ctx, o.s.store, u, o.s.logger)
}

func (o s3Outputs) resolve(r *http.Request, obj store.Upload) (string, time.Time, error) {
	bucket := o.s.downloadStores[store.StorageCache]
	if bucket == nil {
		return "", time.Time{}, fmt.Errorf("%w: no cache bucket to sign", errSigningUnavailable)
	}
	key, err := bucket.Key(string(obj.Team), obj.Provenance+"/"+obj.Key)
	if err != nil {
		return "", time.Time{}, err
	}
	return o.s.signObjectURL(r, bucket, key)
}

type outputUploadRequest struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type outputCommitRequest struct {
	UploadID string `json:"upload_id"`
}

type outputCaller struct {
	team                               store.Team
	principal, claimPrefix, provenance string
}

// safety: a claim token is its node's own attempt on Cloud compute; any
// other caller already passed claimedBy for this node, and its team is the
// run's.
func (s *Server) outputCallerFor(w http.ResponseWriter, r *http.Request) (outputCaller, bool) {
	if tok, ok := claimTokenFromContext(r.Context()); ok {
		return outputCaller{team: tok.Team, principal: tok.RunID + "/" + tok.NodeID, claimPrefix: tok.Prefix, provenance: "cloud"}, true
	}
	team, err := s.runTeam(r)
	if err != nil {
		writeError(w, http.StatusNotFound, runNotFound(r.PathValue("id")))
		return outputCaller{}, false
	}
	c := outputCaller{team: team, principal: "anonymous", provenance: "local"}
	if p, ok := PrincipalFromContext(r.Context()); ok {
		c.principal, c.claimPrefix = p.Name, p.TokenPrefix
		metered, err := s.store.TokenMetered(r.Context(), p.TokenPrefix)
		if err != nil {
			s.writeInternalError(w, r, "output provenance", err)
			return outputCaller{}, false
		}
		if metered {
			c.provenance = "cloud"
		}
	}
	return c, true
}

func (s *Server) handleOutputUpload(w http.ResponseWriter, r *http.Request) {
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	outputs := s.outputs()
	if outputs == nil {
		writeError(w, http.StatusServiceUnavailable, errOutputsUnavailable)
		return
	}
	var req outputUploadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Size <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("an output upload carries at least one byte"))
		return
	}
	if req.Size > store.MaxOutputBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("%w: an output is %d bytes; the limit is %d (64 MiB)",
			store.ErrOutputLimit, req.Size, store.MaxOutputBytes))
		return
	}
	caller, ok := s.outputCallerFor(w, r)
	if !ok {
		return
	}
	key, err := s.store.NodeOutputKey(r.Context(), runID, nodeID)
	if err != nil {
		s.writeInternalError(w, r, "output key", err)
		return
	}
	u, err := s.store.ReserveUpload(r.Context(), store.UploadRequest{
		Team: caller.team, RunID: runID, Kind: store.StorageCache, Key: key, Size: req.Size,
		SHA256: req.SHA256, Principal: caller.principal, ClaimPrefix: caller.claimPrefix, Provenance: caller.provenance,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOutputLimit), errors.Is(err, store.ErrStorageQuota):
			writeError(w, http.StatusRequestEntityTooLarge, err)
		case errors.Is(err, store.ErrTooManyPendingUploads):
			writeError(w, http.StatusTooManyRequests, err)
		case errors.Is(err, store.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, err)
		case s.writeComputeLimitRefusal(w, r, "", "", err):
		default:
			s.writeInternalError(w, r, "reserve output upload", err)
		}
		return
	}
	putURL, headers, err := outputs.grant(r.Context(), u)
	if err != nil {
		if releaseErr := s.store.ReleaseStorage(r.Context(), u.Team, u.ID, time.Now()); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
		s.writeInternalError(w, r, "grant output upload", err)
		return
	}
	if strings.HasPrefix(putURL, "/") {
		putURL = s.externalBaseURL(r) + putURL
	}
	writeJSON(w, http.StatusOK, store.OutputUploadGrant{
		UploadID: u.ID, Key: u.Key, URL: putURL, Headers: headers,
		ExpiresAt: time.Now().Add(directUploadPutTTL).UTC(),
	})
}

func (s *Server) handleOutputCommit(w http.ResponseWriter, r *http.Request) {
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	outputs := s.outputs()
	if outputs == nil {
		writeError(w, http.StatusServiceUnavailable, errOutputsUnavailable)
		return
	}
	var req outputCommitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	caller, ok := s.outputCallerFor(w, r)
	if !ok {
		return
	}
	u, err := s.store.UploadForTeam(r.Context(), caller.team, req.UploadID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "read output upload", err)
		return
	}
	if u.RunID != runID || !strings.HasPrefix(u.Key, store.OutputKeyPrefix(runID, nodeID)) ||
		u.Principal != caller.principal || u.ClaimPrefix != caller.claimPrefix || !time.Now().Before(u.ExpiresAt) {
		writeError(w, http.StatusForbidden, errors.New("this caller cannot commit the upload"))
		return
	}
	if !u.CommittedAt.IsZero() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writeCommitResult(w, r, outputs.commit(r.Context(), u))
}

// safety: a claim reads within its own run only the outputs of its node's
// transitive dependencies, which are the only ones its plan could wait for.
func (s *Server) handleGetNodeOutput(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	n, err := tenant.GetNode(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if n.Status != "done" {
		writeError(w, http.StatusConflict, fmt.Errorf("node %s/%s not finished (status=%s)", runID, nodeID, n.Status))
		return
	}
	team, err := requestTeam(r)
	if err != nil {
		writeError(w, http.StatusNotFound, runNotFound(runID))
		return
	}
	if tok, ok := claimTokenFromContext(r.Context()); ok && runID == tok.RunID {
		ancestor, err := s.store.NodeIsAncestor(r.Context(), tok.Team, runID, nodeID, tok.NodeID)
		if err != nil {
			s.writeInternalError(w, r, "node ancestry", err)
			return
		}
		if !ancestor {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code:    "output_not_ancestor",
				Message: "a claim reads only the outputs of its node's dependencies",
			})
			return
		}
	}
	s.writeOutputGrant(w, r, team, runID, nodeID)
}

func (s *Server) writeOutputGrant(w http.ResponseWriter, r *http.Request, team store.Team, runID, nodeID string) {
	grant, ok := s.outputGrant(w, r, team, runID, nodeID)
	if ok {
		writeJSON(w, http.StatusOK, grant)
	}
}

// safety: a node whose output row is gone, because it recorded none or its
// run passed retention, reads as no output rather than an error.
func (s *Server) outputGrant(w http.ResponseWriter, r *http.Request, team store.Team, runID, nodeID string) (store.OutputReadGrant, bool) {
	obj, err := s.store.NodeOutputObject(r.Context(), team, runID, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return store.OutputReadGrant{SourceRunID: runID}, true
	}
	if err != nil {
		s.writeInternalError(w, r, "read node output", err)
		return store.OutputReadGrant{}, false
	}
	outputs := s.outputs()
	if outputs == nil {
		writeError(w, http.StatusServiceUnavailable, errOutputsUnavailable)
		return store.OutputReadGrant{}, false
	}
	signed, expires, err := outputs.resolve(r, obj)
	if errors.Is(err, errSigningUnavailable) {
		writeError(w, http.StatusServiceUnavailable, err)
		return store.OutputReadGrant{}, false
	}
	if err != nil {
		s.writeInternalError(w, r, "sign node output", err)
		return store.OutputReadGrant{}, false
	}
	return store.OutputReadGrant{
		URL: signed, SHA256: obj.SHA256, Size: obj.Size, Expires: expires, SourceRunID: runID,
	}, true
}
