package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// ReadMemo verifies the output, manifest and every member through an authorized
// reader. It returns the verified output and manifest for reuse or import.
// Empty output is valid; callers enforce the output's application schema.
func ReadMemo(ctx context.Context, get func(context.Context, Digest) (VerifiedReader, error), team string, hit MemoHit) ([]byte, []byte, error) {
	// safety: empty outputs have an explicit record; an absent result must rerun.
	if !hit.Output.Set {
		return nil, nil, ErrNotFound
	}
	if !hit.Output.Value.Valid() || !hit.Manifest.Set && len(hit.Members) != 0 {
		return nil, nil, ErrIntegrity
	}
	var output bytes.Buffer
	if _, err := copyMemoBlob(ctx, get, hit.Output.Value, &output, limits.NodeOutputBytes); err != nil {
		return nil, nil, err
	}
	if !hit.Manifest.Set {
		return output.Bytes(), nil, nil
	}
	var manifest bytes.Buffer
	if _, err := copyMemoBlob(ctx, get, hit.Manifest.Value, &manifest, -1); err != nil {
		return nil, nil, err
	}
	if _, err := DecodeManifest(manifest.Bytes(), hit.Manifest.Value, team, hit.Members); err != nil {
		return nil, nil, err
	}
	verified := make(map[Digest]int64)
	for _, member := range hit.Members {
		size, ok := verified[member.Digest]
		if !ok {
			var err error
			size, err = copyMemoBlob(ctx, get, member.Digest, io.Discard, member.Size)
			if err != nil {
				return nil, nil, err
			}
			verified[member.Digest] = size
		}
		if size != member.Size {
			return nil, nil, ErrIntegrity
		}
	}
	return output.Bytes(), manifest.Bytes(), nil
}

func readMemoBytes(ctx context.Context, bytes Store, authority Authority, team string, hit MemoHit) ([]byte, error) {
	output, _, err := ReadMemo(ctx, func(ctx context.Context, digest Digest) (VerifiedReader, error) {
		return bytes.Get(ctx, GetRequest{Authority: authority, Digest: digest})
	}, team, hit)
	return output, err
}

func verifyMemoBlob(ctx context.Context, bytes Store, authority Authority, digest Digest) (int64, error) {
	return copyMemoBlob(ctx, func(ctx context.Context, digest Digest) (VerifiedReader, error) {
		return bytes.Get(ctx, GetRequest{Authority: authority, Digest: digest})
	}, digest, io.Discard, -1)
}

func copyMemoBlob(ctx context.Context, get func(context.Context, Digest) (VerifiedReader, error), digest Digest, dst io.Writer, limit int64) (size int64, err error) {
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	r, err := get(ctx, digest)
	if r != nil {
		defer func() { err = errors.Join(err, r.Close(), context.Cause(ctx)) }()
	}
	if err != nil {
		return 0, err
	}
	if r == nil {
		return 0, ErrIntegrity
	}
	var src io.Reader = &contextReader{ctx: ctx, reader: r}
	if limit >= 0 && limit < math.MaxInt64 {
		src = io.LimitReader(src, limit+1)
	}
	size, err = io.Copy(dst, src)
	if err == nil && (!r.Verified() || limit >= 0 && size > limit) {
		err = ErrIntegrity
	}
	return size, err
}
