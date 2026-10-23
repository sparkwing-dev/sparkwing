package logseg

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

const maxSegmentStored = limits.LogSegmentUploadBytes

var errBadSegment = errors.New("logseg: segment does not match its recorded digest")

func decodeSegment(stored []byte, seg Segment) ([]byte, error) {
	if seg.SHA256 != "" {
		sum := sha256.Sum256(stored)
		if hex.EncodeToString(sum[:]) != seg.SHA256 {
			return nil, fmt.Errorf("%w: segment %d digest", errBadSegment, seg.N)
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(stored))
	if err != nil {
		return nil, fmt.Errorf("%w: segment %d: %w", errBadSegment, seg.N, err)
	}
	body, err := io.ReadAll(io.LimitReader(zr, maxSegmentStored+1))
	if err != nil {
		return nil, fmt.Errorf("%w: segment %d: %w", errBadSegment, seg.N, err)
	}
	if int64(len(body)) > maxSegmentStored {
		return nil, fmt.Errorf("%w: segment %d decompresses past %d bytes", errBadSegment, seg.N, maxSegmentStored)
	}
	return body, nil
}

func readObject(ctx context.Context, objs SegmentObjects, key string, limit int64) ([]byte, error) {
	rc, err := objs.Get(ctx, key)
	if err != nil {
		if rc != nil {
			err = errors.Join(err, rc.Close())
		}
		return nil, err
	}
	if rc == nil {
		return nil, fmt.Errorf("logseg: object store returned no reader for %s", key)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, limit+1))
	err = errors.Join(err, rc.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", errBadSegment, key, limit)
	}
	return raw, nil
}
