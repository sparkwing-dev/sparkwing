package blob

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

// ReadOutput consumes and verifies a controller-authorized output object.
func (f *Filesystem) ReadOutput(ctx context.Context, object Blob) ([]byte, error) {
	r, err := f.manifestReader(ctx, object.Team, object.Digest)
	return readObjectOutput(r, err, object)
}

// ReadOutput consumes and verifies a controller-authorized output object.
func (s *S3) ReadOutput(ctx context.Context, object Blob) ([]byte, error) {
	r, err := s.manifestReader(ctx, object.Team, object.Digest)
	return readObjectOutput(r, err, object)
}

func readObjectOutput(r *verifiedReader, err error, object Blob) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(r, limits.NodeOutputBytes+1))
	err = errors.Join(err, r.Close(), r.ctx.Err())
	if err != nil {
		return nil, err
	}
	if !r.Verified() || int64(len(body)) != object.Size || object.Size > limits.NodeOutputBytes || !json.Valid(body) {
		return nil, ErrIntegrity
	}
	return body, nil
}
