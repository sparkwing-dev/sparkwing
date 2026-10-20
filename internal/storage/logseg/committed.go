package logseg

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SegmentLookup returns the currently authorized, persisted ranges of one subject.
type SegmentLookup interface {
	ReadLogSegments(context.Context, store.Actor, blob.LogReadRequest) (store.LogSegments, error)
}

// SegmentObjects opens exact persisted object keys; listings cannot authorize logs.
type SegmentObjects interface {
	Get(context.Context, string) (io.ReadCloser, error)
}

// CommittedReader serves persisted segments behind the controller's log reader.
// Object listings and manifests never establish read authority.
type CommittedReader struct {
	Records SegmentLookup
	Objects SegmentObjects
}

// ReadResult retains the exact emitted byte count even when a later read fails,
// so the controller can meter partial responses and report an incomplete tail.
type ReadResult struct {
	Bytes            int64
	Complete         bool
	IncompleteReason string
}

// Read reauthorizes and verifies each committed segment before emitting its requested suffix.
func (r CommittedReader) Read(ctx context.Context, actor store.Actor, request blob.LogReadRequest, out io.Writer) (result ReadResult, err error) {
	if err := request.Subject.Validate(); err != nil {
		return result, err
	}
	if r.Records == nil || r.Objects == nil || out == nil || request.Offset < 0 {
		return result, fmt.Errorf("logseg: read requires records, objects, output and a nonnegative offset")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	view, err := r.Records.ReadLogSegments(ctx, actor, request)
	if err != nil {
		return result, err
	}
	if err := validateRanges(request, view); err != nil {
		return result, err
	}
	result.IncompleteReason = view.IncompleteReason
	for _, seg := range view.Segments {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if seg.Offset+seg.Size <= request.Offset {
			continue
		}
		if result.Bytes > 0 {
			current, err := r.Records.ReadLogSegments(ctx, actor, request)
			if err != nil {
				return result, err
			}
			if err := validateRanges(request, current); err != nil {
				return result, err
			}
			if !slices.Contains(current.Segments, seg) {
				return result, blob.ErrIntegrity
			}
		}
		stored, err := readObject(ctx, r.Objects, seg.ObjectKey, seg.StoredBytes)
		if err != nil {
			return result, err
		}
		if int64(len(stored)) != seg.StoredBytes {
			return result, blob.ErrIntegrity
		}
		body, err := decodeSegment(stored, Segment{N: int(seg.Index), SHA256: seg.Digest})
		if err != nil || int64(len(body)) != seg.Size {
			return result, errors.Join(blob.ErrIntegrity, err)
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		body = body[max(request.Offset-seg.Offset, 0):]
		n, err := out.Write(body)
		if n < 0 || n > len(body) {
			return result, fmt.Errorf("logseg: invalid writer byte count %d", n)
		}
		result.Bytes += int64(n)
		if err != nil {
			return result, err
		}
		if n != len(body) {
			return result, io.ErrShortWrite
		}
	}
	result.Complete = view.Complete
	return result, nil
}

func validateRanges(request blob.LogReadRequest, view store.LogSegments) error {
	want, err := blob.LogSubjectPrefix(ClassStandard, request.Subject)
	got, gotErr := blob.LogSubjectPrefix(ClassStandard, view.Subject)
	if err != nil || gotErr != nil || got != want || (view.Complete && view.IncompleteReason != "") {
		return blob.ErrIntegrity
	}
	var end, index int64
	var prefix string
	for i, seg := range view.Segments {
		parts := strings.SplitN(seg.ObjectKey, "/", 3)
		if len(parts) != 3 || parts[0] != "logs" {
			return blob.ErrIntegrity
		}
		expected, err := blob.LogSubjectPrefix(parts[1], request.Subject)
		if err != nil || seg.Index < 1 || seg.Index > int64(limits.MaxLogSegmentNumber) || seg.Offset < 0 || seg.Size <= 0 || seg.Size > math.MaxInt64-seg.Offset || seg.StoredBytes <= 0 || seg.StoredBytes > limits.LogSegmentUploadBytes ||
			seg.ObjectKey != blob.LogSegmentKey(expected, int(seg.Index)) || (i > 0 && (seg.Offset != end || seg.Index != index+1 || prefix != expected)) ||
			(i == 0 && seg.Offset > request.Offset) {
			return blob.ErrIntegrity
		}
		digest, err := hex.DecodeString(seg.Digest)
		if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != seg.Digest {
			return blob.ErrIntegrity
		}
		end, index, prefix = seg.Offset+seg.Size, seg.Index, expected
	}
	return nil
}
