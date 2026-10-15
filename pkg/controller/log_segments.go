package controller

import (
	"bytes"
	"context"
	"errors"
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
	logSegmentURLTTL    = 15 * time.Minute
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
	if s.s3Logs != nil {
		d := s.s3Logs
		return s3LogObjects{d: d, objs: s3store.NewArtifactStore(d.bucket, d.prefix, d.client)}
	}

	return nil
}

type s3LogObjects struct {
	d    *s3LogStorage
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
