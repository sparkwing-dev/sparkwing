package blob

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ReadManifest verifies tenant CAS bytes before returning members for authorized record admission.
func (f *Filesystem) ReadManifest(ctx context.Context, team string, digest Digest) ([]Member, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := ObjectKey(team, digest)
	if err != nil {
		return nil, err
	}
	file, err := f.root.Open(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return readManifestObject(ctx, file, Blob{Team: team, Digest: digest, Size: info.Size()})
}

// ReadManifest verifies tenant CAS bytes before returning members for authorized record admission.
func (s *S3) ReadManifest(ctx context.Context, team string, digest Digest) ([]Member, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := ObjectKey(team, digest)
	if err != nil {
		return nil, err
	}
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if s3Error(err, "NoSuchKey", "NotFound") {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if object.Body == nil {
		return nil, ErrIntegrity
	}
	if object.ContentLength == nil || *object.ContentLength < 0 {
		return nil, errors.Join(ErrIntegrity, object.Body.Close())
	}
	return readManifestObject(ctx, object.Body, Blob{Team: team, Digest: digest, Size: *object.ContentLength})
}

func readManifestObject(ctx context.Context, body io.ReadCloser, object Blob) ([]Member, error) {
	reader := newVerifiedReader(ctx, body, SignedTransfer{Digest: object.Digest, Size: object.Size})
	raw, err := io.ReadAll(reader)
	err = errors.Join(err, reader.Close(), ctx.Err())
	if err != nil {
		return nil, err
	}
	if !reader.Verified() {
		return nil, ErrIntegrity
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, ErrIntegrity
	}
	members := make([]Member, len(manifest.Entries))
	for i, entry := range manifest.Entries {
		members[i] = Member{Team: object.Team, ManifestDigest: object.Digest, Digest: entry.Digest, Path: entry.Path, Size: entry.Size, Mode: entry.Mode}
	}
	if _, err := DecodeManifest(raw, object.Digest, object.Team, members); err != nil {
		return nil, err
	}
	return members, nil
}
