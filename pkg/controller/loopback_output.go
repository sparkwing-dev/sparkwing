package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the bytes wait here between the upload and the finish that names
// them, then go to the run's state backend, which stores them its own way.
type loopbackOutputs struct {
	signer outputSigner
	mu     sync.Mutex
	staged map[string]*stagedOutput
}

type stagedOutput struct {
	id, key, sha string
	size         int64
	data         []byte
	committed    bool
}

func (o *loopbackOutputs) byID(id string) *stagedOutput {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.staged[id]
}

func (o *loopbackOutputs) take(ref *store.OutputRef) ([]byte, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, st := range o.staged {
		if st.key == ref.Key && st.committed && st.size == ref.Size && st.sha == ref.SHA256 {
			delete(o.staged, id)
			return st.data, true
		}
	}
	return nil, false
}

func (l *Loopback) handleOutputUpload(w http.ResponseWriter, r *http.Request) {
	var req outputUploadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Size <= 0 || req.Size > store.MaxOutputBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("%w: an output is 1 byte to %d bytes (64 MiB)", store.ErrOutputLimit, store.MaxOutputBytes))
		return
	}
	key, err := store.NewOutputKey(r.PathValue("id"), r.PathValue("nodeID"), 0, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	id := hex.EncodeToString(b[:])
	l.outputs.mu.Lock()
	l.outputs.staged[id] = &stagedOutput{id: id, key: key, sha: req.SHA256, size: req.Size}
	l.outputs.mu.Unlock()
	expires := time.Now().Add(directUploadPutTTL).UTC()
	q := l.outputs.signer.sign("put", "", id, expires.Unix())
	writeJSON(w, http.StatusOK, store.OutputUploadGrant{
		UploadID: id, Key: key, URL: "/api/v1/outputs/uploads/" + id + "?" + q.Encode(), ExpiresAt: expires,
	})
}

func (l *Loopback) handleOutputBlobPut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st := l.outputs.byID(id)
	if _, ok := l.outputs.signer.verify(r, "put", id); !ok || st == nil {
		writeError(w, http.StatusForbidden, errors.New("the upload URL is invalid or expired"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, st.size+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != st.size || hex.EncodeToString(sum[:]) != st.sha {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("%w: uploaded bytes do not match the declaration", errUploadMismatch))
		return
	}
	l.outputs.mu.Lock()
	st.data = data
	l.outputs.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (l *Loopback) handleOutputCommit(w http.ResponseWriter, r *http.Request) {
	var req outputCommitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	l.outputs.mu.Lock()
	defer l.outputs.mu.Unlock()
	st := l.outputs.staged[req.UploadID]
	if st == nil || !strings.HasPrefix(st.key, store.OutputKeyPrefix(r.PathValue("id"), r.PathValue("nodeID"))) {
		writeError(w, http.StatusNotFound, errors.New("no such output upload"))
		return
	}
	if st.data == nil {
		writeError(w, http.StatusConflict, fmt.Errorf("%w: the pending upload is absent", errUploadConflict))
		return
	}
	st.committed = true
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleGetNodeOutput(w http.ResponseWriter, r *http.Request) {
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	n, err := l.state.GetNode(r.Context(), runID, nodeID)
	if err != nil {
		writeStateError(w, err)
		return
	}
	if n.Status != "done" {
		writeError(w, http.StatusConflict, fmt.Errorf("node %s/%s not finished (status=%s)", runID, nodeID, n.Status))
		return
	}
	out, err := l.state.GetNodeOutput(r.Context(), runID, nodeID)
	if err != nil {
		writeStateError(w, err)
		return
	}
	if len(out) == 0 {
		writeJSON(w, http.StatusOK, store.OutputReadGrant{SourceRunID: runID})
		return
	}
	sum := sha256.Sum256(out)
	subject := runID + "/" + nodeID
	expires := time.Now().Add(outputReadTTL).UTC()
	q := l.outputs.signer.sign("get", "", subject, expires.Unix())
	writeJSON(w, http.StatusOK, store.OutputReadGrant{
		URL:    "/api/v1/outputs/objects/" + url.PathEscape(runID) + "/" + url.PathEscape(nodeID) + "?" + q.Encode(),
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(out)), Expires: expires, SourceRunID: runID,
	})
}

func (l *Loopback) handleOutputBlobGet(w http.ResponseWriter, r *http.Request) {
	runID, nodeID := r.PathValue("run"), r.PathValue("node")
	if _, ok := l.outputs.signer.verify(r, "get", runID+"/"+nodeID); !ok {
		writeError(w, http.StatusForbidden, errors.New("the download URL is invalid or expired"))
		return
	}
	out, err := l.state.GetNodeOutput(r.Context(), runID, nodeID)
	if err != nil {
		writeStateError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(out)
}
