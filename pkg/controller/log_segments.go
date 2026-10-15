package controller

import (
	"bytes"
	"context"
	"crypto/md5" // #nosec G501 -- S3's Content-MD5 integrity check, not a security digest
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	logSegmentURLTTL    = directUploadPutTTL
	maxLogSegmentNumber = 1_000_000
	maxLogManifestBytes = 4 << 20
	maxLogSegmentBytes  = 8 << 20
	// safety: gzip frames incompressible output a little larger than it came, so a log the sink cut at its raw cap
	// still fits with one segment of headroom.
	maxNodeLogStoredBytes = logseg.MaxNodeBytes + logseg.SegmentBytes
)

var errLogsUnavailable = errors.New("this controller has no object store or output directory to keep log segments in")

type logObjectStore interface {
	logseg.Objects
	putIfAbsent(ctx context.Context, key string, body []byte) error
	deletePrefix(ctx context.Context, prefix string) (int64, error)
	// safety: the URL accepts only a body of exactly size bytes whose base64 MD5 is contentMD5.
	presignPut(ctx context.Context, r *http.Request, team store.Team, key string, size int64, contentMD5 string) (string, map[string]string, error)
	measure(ctx context.Context) (map[store.Team]int64, error)
	// safety: classes are read from the store, not the configuration, so a retired class's logs stay reachable.
	classes(ctx context.Context) ([]string, error)
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

func (o s3LogObjects) classes(ctx context.Context) ([]string, error) {
	root := o.d.prefix + "/logs/"
	var out []string
	var token *string
	for {
		page, err := o.d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(o.d.bucket), Prefix: aws.String(root), Delimiter: aws.String("/"), ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, p := range page.CommonPrefixes {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(aws.ToString(p.Prefix), root), "/"))
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

func (o s3LogObjects) presignPut(ctx context.Context, _ *http.Request, _ store.Team, key string, size int64, contentMD5 string) (string, map[string]string, error) {
	in := &s3.PutObjectInput{
		Bucket: aws.String(o.d.bucket), Key: aws.String(o.d.prefix + "/" + key),
		ContentLength: aws.Int64(size), ContentMD5: aws.String(contentMD5),
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

func (o fsLogObjects) classes(context.Context) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(o.objs.Root, "lo", "logs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, err
}

func (o fsLogObjects) presignPut(_ context.Context, r *http.Request, team store.Team, key string, size int64, contentMD5 string) (string, map[string]string, error) {
	q := o.s.outputSigner.sign("log-segment", string(team), logPutSubject(key, size, contentMD5), time.Now().Add(logSegmentURLTTL).Unix())
	q.Set("size", strconv.FormatInt(size, 10))
	q.Set("md5", contentMD5)
	return o.s.externalBaseURL(r) + "/api/v1/log-segments/" + key + "?" + q.Encode(), map[string]string{"Content-MD5": contentMD5}, nil
}

func logPutSubject(key string, size int64, contentMD5 string) string {
	return key + "\x00" + strconv.FormatInt(size, 10) + "\x00" + contentMD5
}

func sealedOnConflict(err error) error {
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
// fenced-out runner cannot write the new attempt's log; an ended claim writes only its own attempt.
func (s *Server) logWriterAttempt(w http.ResponseWriter, r *http.Request, team store.Team, attempt, generation int64) (store.NodeLog, bool) {
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	current, gen, err := s.store.CurrentNodeAttempt(r.Context(), team, runID, nodeID)
	if fence, ok := store.TriggerClaimFenceFromContext(r.Context()); ok && errors.Is(err, store.ErrNotFound) && nodeID == store.CompileLogNode {
		current, gen, err = 1, fence.ClaimGeneration, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return store.NodeLog{}, false
	}
	if err != nil {
		s.writeInternalError(w, r, "read the node's attempt", err)
		return store.NodeLog{}, false
	}
	tok, isClaim := claimTokenFromContext(r.Context())
	if isClaim && tok.Generation != gen {
		writeError(w, http.StatusConflict, errors.New("the claim no longer holds this node"))
		return store.NodeLog{}, false
	}
	if isClaim && tok.Ended && (attempt != tok.Attempt || generation != gen) {
		writeError(w, http.StatusConflict, fmt.Errorf("the claim ended; only attempt %d generation %d may finish its log", tok.Attempt, gen))
		return store.NodeLog{}, false
	}
	// safety: a writer keeps the attempt its first grant named, so an execution that retries in its own
	// process goes on writing one log; a claim of another generation has fenced it out.
	if attempt != 0 {
		if generation != gen || attempt > current {
			writeError(w, http.StatusConflict, fmt.Errorf("attempt %d generation %d is superseded by attempt %d generation %d",
				attempt, generation, current, gen))
			return store.NodeLog{}, false
		}
		current = attempt
	}
	return store.NodeLog{Team: team, RunID: runID, NodeID: nodeID, Attempt: current, Generation: gen}, true
}

// safety: the segment's size and digests are signed into its URL and charged before the URL leaves, so a writer
// stores exactly the bytes it paid for; asking again for the same segment is free and other bytes are refused.
func (s *Server) handleLogSegments(w http.ResponseWriter, r *http.Request) {
	objs := s.logObjects()
	if objs == nil {
		writeError(w, http.StatusServiceUnavailable, errLogsUnavailable)
		return
	}
	var req store.LogSegmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := checkLogSegmentRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := objectguard.Shared().Ceiling().Allow(); err != nil {
		writeError(w, http.StatusInsufficientStorage, err)
		return
	}
	team, ok := s.logWriterTeam(w, r)
	if !ok {
		return
	}
	want, ok := s.logWriterAttempt(w, r, team, req.Attempt, req.Generation)
	if !ok {
		return
	}
	now := time.Now()
	rec, ok := s.openNodeLog(w, r, want, now)
	if !ok {
		return
	}
	if !rec.SealedAt.IsZero() {
		writeError(w, http.StatusConflict, logseg.ErrSealed)
		return
	}
	grant := store.LogSegmentGrant{
		Attempt: rec.Attempt, Generation: rec.Generation, Prefix: rec.Prefix,
		Watched: s.liveLogs.Watched(rec.RunID, rec.NodeID),
	}
	if req.N == 0 {
		writeJSON(w, http.StatusOK, grant)
		return
	}
	seg := store.NodeLogSegment{N: req.N, StoredBytes: req.Size, MD5: req.MD5, SHA256: req.SHA256}
	switch err := s.store.SignNodeLogSegment(r.Context(), rec, seg, maxNodeLogStoredBytes, s.MultiTeam(), now); {
	case errors.Is(err, store.ErrNodeLogSealed):
		writeError(w, http.StatusConflict, logseg.ErrSealed)
		return
	case errors.Is(err, store.ErrNodeLogSegmentConflict):
		writeError(w, http.StatusConflict, fmt.Errorf("segment %d: %w", req.N, err))
		return
	case errors.Is(err, store.ErrNodeLogCap):
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("%w of %d stored bytes", err, maxNodeLogStoredBytes))
		return
	case err != nil:
		s.writeStorageRefusal(w, r, err)
		return
	}
	u, headers, err := objs.presignPut(r.Context(), r, team, logseg.SegmentKey(rec.Prefix, req.N), req.Size, req.MD5)
	if err != nil {
		s.writeInternalError(w, r, "presign a log segment", err)
		return
	}
	grant.URL, grant.Headers, grant.ExpiresAt = u, headers, now.Add(logSegmentURLTTL).UTC()
	writeJSON(w, http.StatusOK, grant)
}

func checkLogSegmentRequest(req store.LogSegmentRequest) error {
	if req.N < 0 || req.N > maxLogSegmentNumber || req.Attempt < 0 || req.Generation < 0 {
		return fmt.Errorf("n must be a segment number from 0 to %d, and attempt and generation must not be negative", maxLogSegmentNumber)
	}
	if req.N == 0 {
		return nil
	}
	if digest, err := base64.StdEncoding.DecodeString(req.MD5); err != nil || len(digest) != md5.Size {
		return errors.New("md5 must be the segment's base64 MD5")
	}
	if sum, err := hex.DecodeString(req.SHA256); err != nil || len(sum) != sha256.Size {
		return errors.New("sha256 must be the segment's hex SHA-256")
	}
	if req.Size < 1 || req.Size > maxLogSegmentBytes {
		return fmt.Errorf("size must be from 1 to %d bytes", maxLogSegmentBytes)
	}
	return nil
}

// safety: the manifest names only segments whose size and digest the controller signed, so a sealed log never
// claims bytes nobody paid for, and it is written once, so a late node seal and the controller's seal of an
// abandoned attempt cannot both win.
func (s *Server) handleLogManifest(w http.ResponseWriter, r *http.Request) {
	objs := s.logObjects()
	if objs == nil {
		writeError(w, http.StatusServiceUnavailable, errLogsUnavailable)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLogManifestBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("a log manifest is limited to %d bytes: %w", maxLogManifestBytes, err))
		return
	}
	m, err := logseg.ParseManifest(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if m.Subject.Kind != blob.LogJobAttempt || m.Subject.RunID != r.PathValue("id") || m.Subject.Attempt.NodeID != r.PathValue("nodeID") || m.SealedBy != logseg.SealedByNode {
		writeError(w, http.StatusBadRequest, errors.New("the manifest must name this run, node and a writer's attempt, sealed by the node"))
		return
	}
	team, ok := s.logWriterTeam(w, r)
	if !ok {
		return
	}
	if m.Subject.Team != string(team) {
		writeError(w, http.StatusBadRequest, errors.New("the manifest must name the writer's team"))
		return
	}
	want, ok := s.logWriterAttempt(w, r, team, m.Subject.Attempt.Attempt, m.Subject.Attempt.Generation)
	if !ok {
		return
	}
	rec, err := s.store.GetNodeLog(r.Context(), team, want.RunID, want.NodeID, want.Attempt, want.Generation)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "read the node's log", err)
		return
	}
	signed, err := s.store.NodeLogSegments(r.Context(), rec)
	if err != nil {
		s.writeInternalError(w, r, "read the node's signed log segments", err)
		return
	}
	byN := make(map[int]store.NodeLogSegment, len(signed))
	for _, seg := range signed {
		byN[seg.N] = seg
	}
	var stored int64
	for _, seg := range m.Segments {
		if got, ok := byN[seg.N]; !ok || got.StoredBytes != seg.StoredBytes || got.SHA256 != seg.SHA256 {
			writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("the manifest's segment %d is not the one signed for upload", seg.N))
			return
		}
		stored += seg.StoredBytes
	}
	body, err := m.Marshal()
	if err != nil {
		s.writeInternalError(w, r, "encode a log manifest", err)
		return
	}
	switch err := objs.putIfAbsent(r.Context(), logseg.ManifestKey(rec.Prefix), body); {
	case errors.Is(err, logseg.ErrSealed):
		writeError(w, http.StatusPreconditionFailed, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "store a log manifest", err)
		return
	}
	if err := s.store.MarkNodeLogSealed(r.Context(), rec, stored, time.Now()); err != nil {
		s.writeInternalError(w, r, "record the node's log sealed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if want.Prefix, err = logseg.SubjectPrefix(class, nodeLogSubject(want)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return store.NodeLog{}, false
	}
	if rec, err = s.store.OpenNodeLog(r.Context(), want, now); err != nil {
		s.writeInternalError(w, r, "record the node's log", err)
		return store.NodeLog{}, false
	}
	return rec, true
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

// safety: the URL signs the segment's size and MD5, so a holder stores exactly the bytes the controller charged;
// the same segment may be written again with the same bytes.
func (s *Server) handleLogSegmentPut(w http.ResponseWriter, r *http.Request) {
	o, ok := s.logObjects().(fsLogObjects)
	key, q := r.PathValue("key"), r.URL.Query()
	size, err := strconv.ParseInt(q.Get("size"), 10, 64)
	team, signed := s.outputSigner.verify(r, "log-segment", logPutSubject(key, size, q.Get("md5")))
	if !ok || !signed || err != nil || !strings.HasPrefix(key, "logs/") || storage.SafeArtifactKey(key) != nil ||
		!strings.Contains(key, "/teams/"+string(team)+"/") {
		writeError(w, http.StatusForbidden, errors.New("the upload URL is invalid or expired"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, size+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sum := md5.Sum(body)
	if int64(len(body)) != size || base64.StdEncoding.EncodeToString(sum[:]) != q.Get("md5") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("the segment must be the %d bytes whose MD5 the URL signed", size))
		return
	}
	err = o.objs.PutIfAbsent(r.Context(), key, bytes.NewReader(body))
	if errors.Is(err, storage.ErrPreconditionFailed) {
		stored, readErr := o.objs.Get(r.Context(), key)
		if readErr != nil {
			s.writeInternalError(w, r, "read existing log segment", readErr)
			return
		}
		existing, readErr := io.ReadAll(io.LimitReader(stored, size+1))
		readErr = errors.Join(readErr, stored.Close(), r.Context().Err())
		if readErr != nil {
			s.writeInternalError(w, r, "verify existing log segment", readErr)
			return
		}
		if !bytes.Equal(existing, body) {
			writeError(w, http.StatusConflict, blob.ErrIntegrity)
			return
		}
		err = nil
	}
	if err != nil {
		s.writeInternalError(w, r, "store a log segment", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) sealNodeLog(ctx context.Context, objs logObjectStore, rec store.NodeLog, now time.Time) (*logseg.Manifest, error) {
	m, err := logseg.SealAbandoned(ctx, objs, rec.Prefix, logseg.Manifest{
		Subject: new(nodeLogSubject(rec)),
	}, logseg.ReasonRunnerLost, objs.putIfAbsent)
	if err != nil {
		return nil, err
	}
	var stored int64
	for _, seg := range m.Segments {
		stored += seg.StoredBytes
	}
	if err := s.store.MarkNodeLogSealed(ctx, rec, stored, now); err != nil {
		return nil, err
	}
	s.logger.Info("sealed a node log", "run_id", rec.RunID, "node_id", rec.NodeID, "attempt", rec.Attempt,
		"sealed_by", m.SealedBy, "status", m.Status, "segments", len(m.Segments))
	return m, nil
}

func nodeLogSubject(rec store.NodeLog) blob.LogSubject {
	return blob.LogSubject{
		Kind: blob.LogJobAttempt, Team: string(rec.Team), RunID: rec.RunID,
		Attempt: &runspec.AttemptIdentity{Team: string(rec.Team), RunID: rec.RunID, NodeID: rec.NodeID, Attempt: rec.Attempt, Generation: rec.Generation},
	}
}
