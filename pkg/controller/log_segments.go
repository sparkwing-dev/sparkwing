package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var errLogsUnavailable = errors.New("this controller has no log object store")

type logObjectStore interface {
	logseg.Objects
	putIfAbsent(ctx context.Context, key string, body []byte) error
}

func (s *Server) logObjects() logObjectStore {
	if s.s3Logs != nil {
		d := s.s3Logs
		return s3LogObjects{objs: s3store.NewArtifactStore(d.bucket, d.prefix, d.client)}
	}

	return nil
}

type s3LogObjects struct {
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

func sealedOnConflict(err error) error {
	if errors.Is(err, storage.ErrPreconditionFailed) {
		return logseg.ErrSealed
	}
	return err
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
