package blob

import (
	"context"
	"io"
)

func readMemoBytes(ctx context.Context, bytes Store, authority Authority, team string, hit MemoHit) ([]byte, error) {
	// safety: empty outputs have an explicit record; an absent result must rerun.
	if !hit.Output.Set {
		return nil, ErrNotFound
	}
	output, err := readOutputBytes(ctx, bytes, authority, hit.Output.Value)
	if err != nil {
		return nil, err
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
