package controller

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the S3 store presigns and the filesystem store signs URLs back at
// this controller, so node and agent code is the same against both.
type outputStore interface {
	grant(ctx context.Context, u store.Upload) (string, map[string]string, error)
	commit(ctx context.Context, u store.Upload) error
	resolve(r *http.Request, obj store.Upload) (string, time.Time, error)
	deleteObjects(ctx context.Context, team store.Team, objs []store.Upload) error
}

var errOutputsUnavailable = errors.New("this controller has no output store")

// safety: the S3 store takes the cache bucket the direct uploads already
// use; the filesystem store serves a single replica from the store's own
// output directory.
func (s *Server) outputs() outputStore {
	if s.directUploads != nil {
		return s3Outputs{s: s}
	}
	if s.store != nil && s.store.OutputDir() != "" {
		return fsOutputs{s: s, dir: s.store.OutputDir()}
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

func (o s3Outputs) deleteObjects(ctx context.Context, team store.Team, objs []store.Upload) error {
	bucket := o.s.downloadStores[store.StorageCache]
	if bucket == nil {
		return errOutputsUnavailable
	}
	var errs []error
	for _, obj := range objs {
		if err := bucket.Delete(ctx, string(team), obj.Provenance+"/"+obj.Key); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type fsOutputs struct {
	s   *Server
	dir string
}

const outputReadTTL = time.Minute

func (o fsOutputs) pendingPath(id string) string {
	return filepath.Join(o.dir, "outputs", ".pending", id)
}

func (o fsOutputs) grant(_ context.Context, u store.Upload) (string, map[string]string, error) {
	exp := time.Now().Add(directUploadPutTTL).Unix()
	q := o.s.outputSigner.sign("put", string(u.Team), u.ID, exp)
	return "/api/v1/outputs/uploads/" + url.PathEscape(u.ID) + "?" + q.Encode(),
		map[string]string{"Content-Length": strconv.FormatInt(u.Size, 10)}, nil
}

func (o fsOutputs) commit(ctx context.Context, u store.Upload) error {
	pending := o.pendingPath(u.ID)
	final := store.OutputPath(o.dir, u.Key)
	// safety: the key is this upload's own, so a final file with its size and
	// digest is one an earlier try moved there, and a retry commits it or
	// finishes the cleanup that try left undone.
	if err := checkOutputFile(pending, u.Size, u.SHA256); err != nil {
		if !errors.Is(err, errUploadConflict) || checkOutputFile(final, u.Size, u.SHA256) != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
			return err
		}
		if err := os.Rename(pending, final); err != nil {
			return err
		}
	}
	err := o.s.store.CommitUpload(ctx, u.Team, u.ID, u.Principal, time.Now())
	if errors.Is(err, store.ErrFreeStoragePaused) {
		return errors.Join(err, os.Remove(final))
	}
	return err
}

func (o fsOutputs) resolve(_ *http.Request, obj store.Upload) (string, time.Time, error) {
	expires := time.Now().Add(outputReadTTL).UTC()
	q := o.s.outputSigner.sign("get", string(obj.Team), obj.Key, expires.Unix())
	return "/api/v1/outputs/objects/" + obj.Key + "?" + q.Encode(), expires, nil
}

// safety: S3 expires pending/ by lifecycle; here an upload never committed is
// removed once its reservation window has passed.
func (o fsOutputs) sweepPending(now time.Time) error {
	dir := filepath.Dir(o.pendingPath("x"))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && now.Sub(info.ModTime()) > store.DirectUploadTTL {
			err = os.Remove(filepath.Join(dir, e.Name()))
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (o fsOutputs) deleteObjects(_ context.Context, _ store.Team, objs []store.Upload) error {
	var errs []error
	for _, obj := range objs {
		if err := os.Remove(store.OutputPath(o.dir, obj.Key)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func checkOutputFile(path string, size int64, sha string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: the pending upload is absent", errUploadConflict)
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, size+1))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != sha {
		return fmt.Errorf("%w: pending object size or sha256 does not match the declaration", errUploadMismatch)
	}
	return nil
}

// safety: the MAC key never leaves this process, so only the replica that
// signed a URL can honor it, which is the filesystem store's single-replica
// contract.
type outputSigner []byte

// WithOutputSignKey makes this server sign and honor output URLs with key,
// so two servers over one output directory accept each other's URLs.
func (s *Server) WithOutputSignKey(key []byte) *Server {
	s.outputSigner = outputSigner(key)
	return s
}

func newOutputSigner() outputSigner {
	return outputSigner(rand.Text())
}

func (k outputSigner) mac(verb, team, subject string, exp int64) string {
	mac := hmac.New(sha256.New, k)
	fmt.Fprintf(mac, "sparkwing output v1\x00%s\x00%s\x00%s\x00%d", verb, team, subject, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

func (k outputSigner) sign(verb, team, subject string, exp int64) url.Values {
	return url.Values{"team": {team}, "exp": {strconv.FormatInt(exp, 10)}, "sig": {k.mac(verb, team, subject, exp)}}
}

func (k outputSigner) verify(r *http.Request, verb, subject string) (store.Team, bool) {
	q := r.URL.Query()
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	want := k.mac(verb, q.Get("team"), subject, exp)
	return store.Team(q.Get("team")), hmac.Equal([]byte(want), []byte(q.Get("sig")))
}

func (s *Server) handleOutputBlobPut(w http.ResponseWriter, r *http.Request) {
	o, ok := s.outputs().(fsOutputs)
	id := r.PathValue("id")
	team, signed := s.outputSigner.verify(r, "put", id)
	if !ok || !signed {
		writeError(w, http.StatusForbidden, errors.New("the upload URL is invalid or expired"))
		return
	}
	u, err := s.store.UploadForTeam(r.Context(), team, id)
	if err != nil || !u.CommittedAt.IsZero() {
		writeError(w, http.StatusConflict, errors.New("the upload is unknown or already committed"))
		return
	}
	pending := o.pendingPath(u.ID)
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		s.writeInternalError(w, r, "prepare output upload", err)
		return
	}
	f, err := os.OpenFile(pending, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		s.writeInternalError(w, r, "open output upload", err)
		return
	}
	_, err = io.Copy(f, io.LimitReader(r.Body, u.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = checkOutputFile(pending, u.Size, u.SHA256)
	}
	if err != nil {
		if rerr := os.Remove(pending); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			s.logger.Warn("remove refused output upload", "upload_id", u.ID, "err", rerr)
		}
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleOutputBlobGet(w http.ResponseWriter, r *http.Request) {
	o, ok := s.outputs().(fsOutputs)
	key := r.PathValue("key")
	_, signed := s.outputSigner.verify(r, "get", key)
	if !ok || !signed || !strings.HasPrefix(key, "outputs/") || strings.Contains(key, "..") {
		writeError(w, http.StatusForbidden, errors.New("the download URL is invalid or expired"))
		return
	}
	f, err := os.Open(store.OutputPath(o.dir, key))
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "open output", err)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		s.logger.Warn("serve output", "key", key, "err", err)
	}
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
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	n, err := s.store.GetNode(r.Context(), runID, nodeID)
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

// perf: one pass deletes at most this many runs, so a backlog drains over
// several passes rather than holding one.
const outputRetentionBatch = 500

// safety: the refs go first, so nothing new can name a released object; then
// its bytes, then its row, and a run whose bytes could not be deleted keeps
// its retention row and is released again on the next pass.
func (s *Server) pruneExpiredOutputs(ctx context.Context, now time.Time) error {
	outputs := s.outputs()
	if outputs == nil {
		return nil
	}
	var errs []error
	if fs, ok := outputs.(fsOutputs); ok {
		errs = append(errs, fs.sweepPending(now))
	}
	expired, err := s.store.ExpiredOutputRuns(ctx, now, outputRetentionBatch)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, run := range expired {
		objs, err := s.store.ReleaseRunOutputs(ctx, run.Team, run.RunID)
		if err != nil {
			errs = append(errs, fmt.Errorf("release outputs of %s: %w", run.RunID, err))
			continue
		}
		if err := outputs.deleteObjects(ctx, run.Team, objs); err != nil {
			errs = append(errs, fmt.Errorf("delete outputs of %s: %w", run.RunID, err))
			continue
		}
		if err := s.store.ForgetRunOutputs(ctx, run.Team, run.RunID, objs); err != nil {
			errs = append(errs, fmt.Errorf("drop output rows of %s: %w", run.RunID, err))
		}
	}
	return errors.Join(errs...)
}

// safety: a controller with a storage pass prunes outputs in that leased
// pass; one without has a filesystem store, which is single-replica, so the
// reaper prunes at the same hourly cadence.
func (s *Server) pruneOutputsWithoutStoragePass(ctx context.Context, now time.Time) {
	if s.storagePass != nil || now.Sub(s.lastOutputPrune) < StoragePassEvery {
		return
	}
	s.lastOutputPrune = now
	if err := s.pruneExpiredOutputs(ctx, now); err != nil {
		s.logger.Error("prune expired outputs", "err", err)
	}
}
