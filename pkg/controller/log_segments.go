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
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
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

var errLogsUnavailable = errors.New("this controller has no log object store")

type logObjectStore interface {
	logseg.Objects
	putIfAbsent(ctx context.Context, key string, body []byte) error
	// safety: the URL accepts only a body of exactly size bytes whose base64 MD5 is contentMD5.
	presignPut(ctx context.Context, r *http.Request, team store.Team, key string, size int64, contentMD5 string) (string, map[string]string, error)
}

func (s *Server) logObjects() logObjectStore {
	if s.directUploads != nil {
		d := s.directUploads
		return s3LogObjects{d: d, objs: s3store.NewArtifactStore(d.bucket, d.prefix, d.client)}
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

func sealedOnConflict(err error) error {
	if errors.Is(err, storage.ErrPreconditionFailed) {
		return logseg.ErrSealed
	}
	return err
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

// safety: the URL signs the segment's size and MD5, so a holder stores exactly the bytes the controller charged;
// the same segment may be written again with the same bytes.
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
