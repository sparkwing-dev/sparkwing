package acceptance

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckBlobLogRecords joins physical signed uploads to persisted, authorized log ranges.
func CheckBlobLogRecords(ctx context.Context, root string, transport blob.LogTransport, objects logseg.SegmentObjects, transfers blob.HTTPClient, seed [16]byte, count int) (err error) {
	path := filepath.Join(root, "physical-logs.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	commit, err := seedStoreLogBoundary(ctx, s.DB())
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	uploader, err := logseg.NewSubjectUploader(logseg.Data{Subject: commit.Subject, Transport: transport, HTTPClient: transfers, Records: tenant, Actor: commit.Actor})
	if err != nil {
		return err
	}
	var segments []logseg.Segment
	var content []byte
	for n := 1; n <= count; n++ {
		line := []byte(fmt.Sprintf("%x/%d\n", seed, n))
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(line); err != nil {
			return errors.Join(err, writer.Close())
		}
		if err := writer.Close(); err != nil {
			return err
		}
		sum := sha256.Sum256(compressed.Bytes())
		segment := logseg.Segment{N: n, FirstSeq: int64(n), LastSeq: int64(n), Lines: 1, Bytes: int64(len(line)), StoredBytes: int64(compressed.Len()), SHA256: hex.EncodeToString(sum[:])}
		if err := uploader.PutSegment(ctx, segment, compressed.Bytes()); err != nil {
			return fmt.Errorf("persist physical segment %d: %w", n, err)
		}
		segments, content = append(segments, segment), append(content, line...)
	}
	manifest := logseg.Manifest{Status: logseg.StatusComplete, Segments: segments}
	for range 2 {
		if err := uploader.PutManifest(ctx, manifest); err != nil {
			return fmt.Errorf("seal physical log: %w", err)
		}
	}
	reader, err := seedStoreLogReader(ctx, s.DB(), commit.Actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	committed := logseg.CommittedReader{Records: tenant, Objects: objects}
	for _, offset := range []int64{0, 1, int64(len(content) / 2), int64(len(content)), int64(len(content) + 1)} {
		var out bytes.Buffer
		result, err := committed.Read(ctx, reader, blob.LogReadRequest{Subject: commit.Subject, Offset: offset}, &out)
		want := content[min(int(offset), len(content)):]
		if err != nil || !result.Complete || result.Bytes != int64(len(want)) || !bytes.Equal(out.Bytes(), want) {
			return fmt.Errorf("reopened physical log at %d: %+v, %q: %w", offset, result, out.Bytes(), err)
		}
	}
	retained, err := tenant.StorageRetainedBytes(ctx)
	if err != nil || retained != int64(len(content)) {
		return fmt.Errorf("physical log retained bytes=%d, want=%d: %w", retained, len(content), err)
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE capabilities SET revoked_at=1 WHERE prefix=?", reader.Authority.ID); err != nil {
		return err
	}
	var out bytes.Buffer
	result, err := committed.Read(ctx, reader, blob.LogReadRequest{Subject: commit.Subject}, &out)
	if err == nil || result.Bytes != 0 || out.Len() != 0 {
		return fmt.Errorf("revoked reader consumed a physical log: %+v: %v", result, err)
	}
	return nil
}
