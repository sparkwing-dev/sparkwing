package blob

import (
	"context"
	"errors"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// VerifyManifest verifies tenant CAS bytes against submitted members before record admission.
func (f *Filesystem) VerifyManifest(ctx context.Context, team string, digest Digest, members []Member) error {
	reader, err := f.manifestReader(ctx, team, digest)
	if err != nil {
		return err
	}
	return finishManifest(reader, verifyManifestStream(reader, Blob{Team: team, Digest: digest}, members))
}

// VerifyManifest verifies tenant CAS bytes against submitted members before record admission.
func (s *S3) VerifyManifest(ctx context.Context, team string, digest Digest, members []Member) error {
	reader, err := s.manifestReader(ctx, team, digest)
	if err != nil {
		return err
	}
	return finishManifest(reader, verifyManifestStream(reader, Blob{Team: team, Digest: digest}, members))
}

// ReadManifest reads members of an admitted manifest through verified tenant CAS bytes.
func (f *Filesystem) ReadManifest(ctx context.Context, team string, digest Digest) ([]Member, error) {
	reader, err := f.manifestReader(ctx, team, digest)
	if err != nil {
		return nil, err
	}
	return readManifestMembers(reader, team, digest)
}

// ReadManifest reads members of an admitted manifest through verified tenant CAS bytes.
func (s *S3) ReadManifest(ctx context.Context, team string, digest Digest) ([]Member, error) {
	reader, err := s.manifestReader(ctx, team, digest)
	if err != nil {
		return nil, err
	}
	return readManifestMembers(reader, team, digest)
}

func (f *Filesystem) manifestReader(ctx context.Context, team string, digest Digest) (*verifiedReader, error) {
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
	return newVerifiedReader(ctx, file, SignedTransfer{Digest: digest, Size: info.Size()}), nil
}

func (s *S3) manifestReader(ctx context.Context, team string, digest Digest) (*verifiedReader, error) {
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
	return newVerifiedReader(ctx, object.Body, SignedTransfer{Digest: digest, Size: *object.ContentLength}), nil
}

func readManifestMembers(reader *verifiedReader, team string, digest Digest) ([]Member, error) {
	members := []Member{}
	paths := map[string]bool{}
	err := walkManifestStream(&manifestJSONReader{reader: reader}, func(entry ManifestEntry) error {
		if entry.Size < 0 || !validDigest(entry.Digest) || paths[entry.Path] {
			return ErrIntegrity
		}
		paths[entry.Path] = true
		members = append(members, Member{Team: team, ManifestDigest: digest, Digest: entry.Digest, Path: entry.Path, Size: entry.Size, Mode: entry.Mode})
		return nil
	})
	if err := finishManifest(reader, err); err != nil {
		return nil, err
	}
	return members, nil
}

func finishManifest(reader *verifiedReader, err error) error {
	if err != nil {
		err = errors.Join(ErrIntegrity, reader.err)
	}
	err = errors.Join(err, reader.Close(), reader.ctx.Err())
	if err != nil {
		return err
	}
	if !reader.Verified() {
		return ErrIntegrity
	}
	return nil
}
