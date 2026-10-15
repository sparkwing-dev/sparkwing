package controller

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type outputStore interface {
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
		ancestor, err := tenant.NodeIsAncestor(r.Context(), runID, nodeID, tok.NodeID)
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
