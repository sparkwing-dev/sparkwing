package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// perf: a batch of 16 lasts at least 160 s at the 10 s cadence.
const (
	logSegmentBatch     = 16
	logSegmentURLTTL    = directUploadPutTTL
	maxLogSegmentNumber = 1_000_000
	maxLogManifestBytes = 4 << 20
	maxLogSegmentBytes  = 8 << 20
)

var errLogsUnavailable = errors.New("this controller has no object store or output directory to keep log segments in")

type logObjectStore interface {
	logseg.Objects
	putIfAbsent(ctx context.Context, key string, body []byte) error
	deletePrefix(ctx context.Context, prefix string) (int64, error)
	presignPut(ctx context.Context, r *http.Request, team store.Team, key string, manifest bool) (string, map[string]string, error)
	measure(ctx context.Context) (map[store.Team]int64, error)
}

func logObjectTeam(key string) (store.Team, bool) {
	parts := strings.SplitN(key, "/", 5)
	if len(parts) < 5 || parts[0] != "logs" || parts[2] != "teams" {
		return "", false
	}
	return store.Team(parts[3]), true
}

// safety: the S3 store presigns and the filesystem store signs URLs back at
// this controller, so the runner side is the same against both.
func (s *Server) logObjects() logObjectStore {
	if s.directUploads != nil {
		d := s.directUploads
		return s3LogObjects{d: d, objs: s3store.NewArtifactStore(d.bucket, d.prefix, d.client)}
	}
	if s.store != nil && s.store.OutputDir() != "" {
		objs, err := fs.NewArtifactStore(s.store.OutputDir())
		if err != nil {
			s.logger.Error("open the log segment directory", "dir", s.store.OutputDir(), "err", err)
			return nil
		}
		return fsLogObjects{s: s, objs: objs}
	}
	return nil
}

type s3LogObjects struct {
	d    *directUploadS3
	objs *s3store.ArtifactStore
}

func (o s3LogObjects) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return o.objs.Get(ctx, key)
}

func (o s3LogObjects) List(ctx context.Context, prefix string) ([]string, error) {
	return o.objs.List(ctx, prefix)
}

func (o s3LogObjects) putIfAbsent(ctx context.Context, key string, body []byte) error {
	return sealedOnConflict(o.objs.PutIfAbsent(ctx, key, bytes.NewReader(body)))
}

// safety: the listing carries each object's size, so the bytes reported are the ones removed, which is what a
// prune counts against a team's overage.
func (o s3LogObjects) deletePrefix(ctx context.Context, prefix string) (int64, error) {
	root := o.d.prefix + "/"
	var deleted int64
	var errs []error
	var token *string
	for {
		page, err := o.d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(o.d.bucket), Prefix: aws.String(root + prefix), ContinuationToken: token,
		})
		if err != nil {
			return deleted, errors.Join(append(errs, err)...)
		}
		for _, obj := range page.Contents {
			if err := o.objs.Delete(ctx, strings.TrimPrefix(aws.ToString(obj.Key), root)); err != nil {
				errs = append(errs, err)
				continue
			}
			deleted += aws.ToInt64(obj.Size)
		}
		if !aws.ToBool(page.IsTruncated) {
			return deleted, errors.Join(errs...)
		}
		token = page.NextContinuationToken
	}
}

func (o s3LogObjects) measure(ctx context.Context) (map[store.Team]int64, error) {
	out := map[store.Team]int64{}
	root := o.d.prefix + "/"
	var token *string
	for {
		page, err := o.d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(o.d.bucket), Prefix: aws.String(root + "logs/"), ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			if team, ok := logObjectTeam(strings.TrimPrefix(aws.ToString(obj.Key), root)); ok {
				out[team] += aws.ToInt64(obj.Size)
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

func (o s3LogObjects) presignPut(ctx context.Context, _ *http.Request, _ store.Team, key string, manifest bool) (string, map[string]string, error) {
	in := &s3.PutObjectInput{Bucket: aws.String(o.d.bucket), Key: aws.String(o.d.prefix + "/" + key)}
	if manifest {
		in.IfNoneMatch = aws.String("*")
	}
	put, err := o.d.presign.PresignPutObject(ctx, in, func(p *s3.PresignOptions) { p.Expires = logSegmentURLTTL })
	if err != nil {
		return "", nil, err
	}
	headers := map[string]string{}
	for k, v := range put.SignedHeader {
		if len(v) > 0 && !strings.EqualFold(k, "Host") {
			headers[k] = v[0]
		}
	}
	return put.URL, headers, nil
}

type fsLogObjects struct {
	s    *Server
	objs *fs.ArtifactStore
}

func (o fsLogObjects) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return o.objs.Get(ctx, key)
}

func (o fsLogObjects) List(ctx context.Context, prefix string) ([]string, error) {
	return o.objs.List(ctx, prefix)
}

func (o fsLogObjects) putIfAbsent(ctx context.Context, key string, body []byte) error {
	return sealedOnConflict(o.objs.PutIfAbsent(ctx, key, bytes.NewReader(body)))
}

func (o fsLogObjects) deletePrefix(ctx context.Context, prefix string) (int64, error) {
	return deleteListed(ctx, o.objs, prefix)
}

// hack: the filesystem store shards a key under its first two characters, and every log key begins
// "logs/", so the log objects all sit under lo/logs/ beneath its root.
func (o fsLogObjects) measure(context.Context) (map[store.Team]int64, error) {
	out := map[store.Team]int64{}
	root := filepath.Join(o.objs.Root, "lo")
	err := filepath.WalkDir(filepath.Join(root, "logs"), func(path string, d iofs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if team, ok := logObjectTeam(filepath.ToSlash(rel)); ok {
			out[team] += info.Size()
		}
		return nil
	})
	return out, err
}

func (o fsLogObjects) presignPut(_ context.Context, r *http.Request, team store.Team, key string, manifest bool) (string, map[string]string, error) {
	q := o.s.outputSigner.sign(logPutVerb(manifest), string(team), key, time.Now().Add(logSegmentURLTTL).Unix())
	return o.s.externalBaseURL(r) + "/api/v1/log-segments/" + key + "?" + q.Encode(), nil, nil
}

func logPutVerb(manifest bool) string {
	if manifest {
		return "log-manifest"
	}
	return "log-segment"
}

func sealedOnConflict(_ storage.ETag, err error) error {
	if errors.Is(err, storage.ErrPreconditionFailed) {
		return logseg.ErrSealed
	}
	return err
}

// safety: each object's size is read before it goes, so the bytes reported are the ones removed.
func deleteListed(ctx context.Context, objs storage.ArtifactStore, prefix string) (int64, error) {
	keys, err := objs.List(ctx, prefix)
	if err != nil {
		return 0, err
	}
	var deleted int64
	var errs []error
	for _, k := range keys {
		size, err := objectSize(ctx, objs, k)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := objs.Delete(ctx, k); err != nil {
			errs = append(errs, err)
			continue
		}
		deleted += size
	}
	return deleted, errors.Join(errs...)
}

func objectSize(ctx context.Context, objs storage.ArtifactStore, key string) (int64, error) {
	body, err := objs.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	defer func() { _ = body.Close() }()
	if f, ok := body.(interface{ Stat() (os.FileInfo, error) }); ok {
		if info, err := f.Stat(); err == nil {
			return info.Size(), nil
		}
	}
	return io.Copy(io.Discard, body)
}

// safety: the attempt is the one the node's claim holds now, and a writer naming an older one is refused, so a
// fenced-out runner cannot write the new attempt's log.
func (s *Server) handleLogSegments(w http.ResponseWriter, r *http.Request) {
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	objs := s.logObjects()
	if objs == nil {
		writeError(w, http.StatusServiceUnavailable, errLogsUnavailable)
		return
	}
	var req store.LogSegmentsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Next < 1 || req.Next > maxLogSegmentNumber || req.Count < 0 || req.UploadedThrough < 0 || req.StoredBytes < 0 {
		writeError(w, http.StatusBadRequest, errors.New("next must be a segment number from 1, and count, uploaded_through and stored_bytes must not be negative"))
		return
	}
	count := min(req.Count, logSegmentBatch, maxLogSegmentNumber-req.Next+1)
	if err := objectguard.Shared().Ceiling().Allow(); err != nil {
		writeError(w, http.StatusInsufficientStorage, err)
		return
	}
	team, ok := s.logWriterTeam(w, r)
	if !ok {
		return
	}
	attempt, generation, err := s.store.CurrentNodeAttempt(r.Context(), team, runID, nodeID)
	if fence, ok := store.TriggerClaimFenceFromContext(r.Context()); ok && errors.Is(err, store.ErrNotFound) && nodeID == store.CompileLogNode {
		attempt, generation, err = 1, fence.ClaimGeneration, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "read the node's attempt", err)
		return
	}
	if tok, isClaim := claimTokenFromContext(r.Context()); isClaim && tok.Generation != generation {
		writeError(w, http.StatusConflict, errors.New("the claim no longer holds this node"))
		return
	}
	// safety: a writer keeps the attempt its first grant named, so an execution that retries in its own
	// process goes on writing one log; a claim of another generation has fenced it out.
	if req.Attempt != 0 {
		if req.Generation != generation || req.Attempt > attempt {
			writeError(w, http.StatusConflict, fmt.Errorf("attempt %d generation %d is superseded by attempt %d generation %d",
				req.Attempt, req.Generation, attempt, generation))
			return
		}
		attempt = req.Attempt
	}
	now := time.Now()
	rec, ok := s.openNodeLog(w, r, store.NodeLog{Team: team, RunID: runID, NodeID: nodeID, Attempt: attempt, Generation: generation}, now)
	if !ok {
		return
	}
	if !rec.SealedAt.IsZero() {
		writeError(w, http.StatusConflict, logseg.ErrSealed)
		return
	}
	rec.UploadedThrough, rec.StoredBytes = int64(req.UploadedThrough), req.StoredBytes
	if err := s.store.RecordNodeLogProgress(r.Context(), rec, now); err != nil {
		s.writeInternalError(w, r, "record the node's log progress", err)
		return
	}
	if err := s.chargeNodeLog(r.Context(), rec, req.StoredBytes, now); err != nil {
		s.writeStorageRefusal(w, r, err)
		return
	}
	grant := store.LogSegmentsGrant{
		Attempt: attempt, Generation: generation, Prefix: rec.Prefix,
		ExpiresAt: now.Add(logSegmentURLTTL).UTC(), Watched: s.liveLogs.Watched(runID, nodeID),
	}
	for n := req.Next; n < req.Next+count; n++ {
		u, headers, err := objs.presignPut(r.Context(), r, team, logseg.SegmentKey(rec.Prefix, n), false)
		if err != nil {
			s.writeInternalError(w, r, "presign a log segment", err)
			return
		}
		grant.URLs = append(grant.URLs, store.LogSegmentURL{N: n, URL: u, Headers: headers})
	}
	u, headers, err := objs.presignPut(r.Context(), r, team, logseg.ManifestKey(rec.Prefix), true)
	if err != nil {
		s.writeInternalError(w, r, "presign a log manifest", err)
		return
	}
	grant.ManifestURL = store.LogSegmentURL{URL: u, Headers: headers}
	writeJSON(w, http.StatusOK, grant)
}

// safety: the team's standing on an attempt's first request picks its retention class, and a team whose storage
// is paused, or a class no lifecycle rule expires, takes no log rather than one nothing would bill or expire.
func (s *Server) openNodeLog(w http.ResponseWriter, r *http.Request, want store.NodeLog, now time.Time) (store.NodeLog, bool) {
	rec, err := s.store.GetNodeLog(r.Context(), want.Team, want.RunID, want.NodeID, want.Attempt, want.Generation)
	if err == nil {
		return rec, true
	}
	if !errors.Is(err, store.ErrNotFound) {
		s.writeInternalError(w, r, "read the node's log", err)
		return store.NodeLog{}, false
	}
	tier := store.TeamTierFunded
	if s.MultiTeam() {
		res, err := s.store.ReserveStorage(r.Context(), store.StorageReserve{
			Team: want.Team, Kind: store.StorageLogs, UpTo: true, Now: now,
		})
		if err != nil {
			s.writeStorageRefusal(w, r, err)
			return store.NodeLog{}, false
		}
		tier = res.Tier
		if err := s.store.ReleaseStorage(r.Context(), want.Team, res.ID, now); err != nil {
			s.logger.Warn("release a log standing probe", "team", string(want.Team), "err", err)
		}
	}
	class := s.logClassFor(want.Team, tier)
	if err := s.logClassReady(r.Context(), class); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return store.NodeLog{}, false
	}
	if want.Prefix, err = logseg.AttemptPrefix(class, string(want.Team), want.RunID, want.NodeID, want.Attempt, want.Generation); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return store.NodeLog{}, false
	}
	if rec, err = s.store.OpenNodeLog(r.Context(), want, now); err != nil {
		s.writeInternalError(w, r, "record the node's log", err)
		return store.NodeLog{}, false
	}
	return rec, true
}

// safety: the writer reports what it stored, and the delta is claimed in the
// record before it is committed, so two requests never charge it twice; a
// commit the team's storage refuses hands the claim back, so a retry is
// refused again instead of passing on the checkpoint.
func (s *Server) chargeNodeLog(ctx context.Context, rec store.NodeLog, stored int64, now time.Time) error {
	if !s.MultiTeam() || stored <= rec.ChargedBytes {
		return nil
	}
	claimed, err := s.store.ChargeNodeLog(ctx, rec, stored)
	if err != nil || !claimed {
		return err
	}
	err = s.store.CommitStorage(ctx, store.StorageCommit{
		Team: rec.Team, Kind: store.StorageLogs, Bytes: stored - rec.ChargedBytes, Now: now,
	})
	if err == nil {
		return nil
	}
	charged := rec
	charged.ChargedBytes = stored
	if _, rerr := s.store.ChargeNodeLog(context.WithoutCancel(ctx), charged, rec.ChargedBytes); rerr != nil {
		return errors.Join(err, fmt.Errorf("hand back the log charge: %w", rerr))
	}
	return err
}

func (s *Server) writeStorageRefusal(w http.ResponseWriter, r *http.Request, err error) {
	var quota *store.StorageQuotaError
	switch {
	case errors.As(err, &quota):
		writeError(w, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, store.ErrFreeStoragePaused):
		writeError(w, http.StatusPaymentRequired, err)
	default:
		s.writeInternalError(w, r, "charge the node's log storage", err)
	}
}

// safety: a claim token is its node's own attempt; any other caller already
// passed claimedBy for this node, and its team is the run's.
func (s *Server) logWriterTeam(w http.ResponseWriter, r *http.Request) (store.Team, bool) {
	if tok, ok := claimTokenFromContext(r.Context()); ok {
		return tok.Team, true
	}
	team, err := s.runTeam(r)
	if err != nil {
		writeError(w, http.StatusNotFound, runNotFound(r.PathValue("id")))
		return "", false
	}
	return team, true
}

// safety: a segment may be written again with the same bytes, but a manifest only once, so a late node seal and
// the controller's seal of an abandoned attempt cannot both win.
func (s *Server) handleLogSegmentPut(w http.ResponseWriter, r *http.Request) {
	o, ok := s.logObjects().(fsLogObjects)
	key := r.PathValue("key")
	manifest := strings.HasSuffix(key, "/"+logseg.ManifestName)
	team, signed := s.outputSigner.verify(r, logPutVerb(manifest), key)
	if !ok || !signed || !strings.HasPrefix(key, "logs/") || storage.SafeArtifactKey(key) != nil ||
		!strings.Contains(key, "/teams/"+string(team)+"/") {
		writeError(w, http.StatusForbidden, errors.New("the upload URL is invalid or expired"))
		return
	}
	limit := int64(maxLogSegmentBytes)
	if manifest {
		limit = maxLogManifestBytes
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if int64(len(body)) > limit {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("a log object is limited to %d bytes", limit))
		return
	}
	if manifest {
		err = o.putIfAbsent(r.Context(), key, body)
	} else {
		err = o.objs.Put(r.Context(), key, bytes.NewReader(body))
	}
	switch {
	case errors.Is(err, logseg.ErrSealed):
		writeError(w, http.StatusPreconditionFailed, err)
	case err != nil:
		s.writeInternalError(w, r, "store a log object", err)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// safety: without this an attempt whose runner is gone would read as streaming forever rather than incomplete.
func (s *Server) sealAbandonedLogs(ctx context.Context, now time.Time) {
	objs := s.logObjects()
	if objs == nil {
		return
	}
	abandoned, err := s.store.AbandonedNodeLogs(ctx, now.Add(-logseg.SealGrace), 100)
	if err != nil {
		s.logger.Error("list abandoned node logs", "err", err)
		return
	}
	for _, rec := range abandoned {
		if _, err := s.sealNodeLog(ctx, objs, rec, now); err != nil {
			s.logger.Warn("seal an abandoned node log", "run_id", rec.RunID, "node_id", rec.NodeID,
				"attempt", rec.Attempt, "err", err)
		}
	}
}

func (s *Server) sealNodeLog(ctx context.Context, objs logObjectStore, rec store.NodeLog, now time.Time) (*logseg.Manifest, error) {
	m, err := logseg.SealAbandoned(ctx, objs, rec.Prefix, logseg.Manifest{
		Run: rec.RunID, Node: rec.NodeID, Attempt: rec.Attempt, Generation: rec.Generation,
	}, logseg.ReasonRunnerLost, objs.putIfAbsent)
	if err != nil {
		return nil, err
	}
	var stored int64
	for _, seg := range m.Segments {
		stored += seg.StoredBytes
	}
	if err := s.chargeNodeLog(ctx, rec, stored, now); err != nil {
		s.logger.Warn("charge a sealed node log", "run_id", rec.RunID, "node_id", rec.NodeID, "err", err)
	}
	if err := s.store.MarkNodeLogSealed(ctx, rec, stored, now); err != nil {
		return nil, err
	}
	s.logger.Info("sealed a node log", "run_id", rec.RunID, "node_id", rec.NodeID, "attempt", rec.Attempt,
		"sealed_by", m.SealedBy, "status", m.Status, "segments", len(m.Segments))
	return m, nil
}
