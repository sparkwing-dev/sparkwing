package blob

import (
	"context"
	"fmt"
	"io"
)

// MaterializedMemo contains output verified after the store authorized and copied
// every result edge. Found stays false if any required bytes cannot be read.
type MaterializedMemo struct {
	Hit    MemoHit
	Output []byte
}

// MaterializeMemo applies the memo miss policy to leaders, followers and retries.
// Records owns the atomic copy; bytes are checked even without a local artifact
// backend. A caller must commit its memoized transition only after a found result.
func MaterializeMemo(ctx context.Context, bytes Store, records RecordStore, request CopyMemoRequest) (MaterializedMemo, error) {
	if err := ctx.Err(); err != nil {
		return MaterializedMemo{}, err
	}
	if bytes == nil || records == nil {
		return MaterializedMemo{}, fmt.Errorf("memo requires records and bytes")
	}
	if request.Lookup.Budget.Deadline.IsZero() {
		return MaterializedMemo{}, nil
	}
	readCtx, cancel := context.WithDeadline(ctx, request.Lookup.Budget.Deadline)
	defer cancel()
	if readCtx.Err() != nil {
		return MaterializedMemo{}, ctx.Err()
	}
	hit, err := records.CopyMemo(readCtx, request)
	if err != nil || !hit.Found {
		return MaterializedMemo{}, ctx.Err()
	}
	output, err := readMemoBytes(readCtx, bytes, request.Lookup.Authority, request.Destination.Team, hit)
	if err != nil || readCtx.Err() != nil {
		return MaterializedMemo{}, ctx.Err()
	}
	return MaterializedMemo{Hit: hit, Output: output}, nil
}

func readMemoBytes(ctx context.Context, bytes Store, authority Authority, team string, hit MemoHit) ([]byte, error) {
	var output []byte
	if hit.Output.Set {
		var err error
		output, err = readOutputBytes(ctx, bytes, authority, hit.Output.Value)
		if err != nil {
			return nil, err
		}
	}
	if !hit.Manifest.Set {
		if len(hit.Members) != 0 {
			return nil, ErrIntegrity
		}
		return output, nil
	}
	r, err := bytes.Get(ctx, GetRequest{Authority: authority, Digest: hit.Manifest.Value})
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(r)
	verifiedManifest := r.Verified()
	closeErr := r.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !verifiedManifest {
		return nil, ErrIntegrity
	}
	if _, err := DecodeManifest(body, hit.Manifest.Value, team, hit.Members); err != nil {
		return nil, err
	}
	verified := make(map[Digest]int64)
	for _, member := range hit.Members {
		if member.ManifestDigest != hit.Manifest.Value || member.Size < 0 {
			return nil, ErrIntegrity
		}
		size, ok := verified[member.Digest]
		if !ok {
			var err error
			size, err = verifyMemoBlob(ctx, bytes, authority, member.Digest)
			if err != nil {
				return nil, err
			}
			verified[member.Digest] = size
		}
		if size != member.Size {
			return nil, ErrIntegrity
		}
	}
	return output, nil
}

func verifyMemoBlob(ctx context.Context, bytes Store, authority Authority, digest Digest) (int64, error) {
	r, err := bytes.Get(ctx, GetRequest{Authority: authority, Digest: digest})
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(io.Discard, r)
	verified := r.Verified()
	closeErr := r.Close()
	if err != nil {
		return n, err
	}
	if closeErr != nil {
		return n, closeErr
	}
	if !verified {
		return n, ErrIntegrity
	}
	return n, nil
}
