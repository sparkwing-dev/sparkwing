package objectguard

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3API is the subset of the S3 client Sparkwing's stores call. It
// mirrors pkg/storage/s3.API; declaring it here keeps the guard free of
// a dependency on the store package.
type S3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// GuardS3 wraps client so every request it sends first spends budget on
// l. A nil limiter returns the client unchanged.
func GuardS3(l *Limiter, client S3API) S3API {
	if l == nil {
		return client
	}
	return &guardedS3{limiter: l, inner: client}
}

type guardedS3 struct {
	limiter *Limiter
	inner   S3API
}

var _ S3API = (*guardedS3)(nil)

func (g *guardedS3) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if err := g.limiter.Allow(ClassGet); err != nil {
		return nil, err
	}
	return g.inner.GetObject(ctx, params, optFns...)
}

// HeadObject spends GET budget because object stores bill a HEAD at the
// GET rate.
func (g *guardedS3) HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if err := g.limiter.Allow(ClassGet); err != nil {
		return nil, err
	}
	return g.inner.HeadObject(ctx, params, optFns...)
}

func (g *guardedS3) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := g.limiter.Allow(ClassPut); err != nil {
		return nil, err
	}
	return g.inner.PutObject(ctx, params, optFns...)
}

func (g *guardedS3) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if err := g.limiter.Allow(ClassDelete); err != nil {
		return nil, err
	}
	return g.inner.DeleteObject(ctx, params, optFns...)
}

func (g *guardedS3) DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	if err := g.limiter.Allow(ClassDelete); err != nil {
		return nil, err
	}
	return g.inner.DeleteObjects(ctx, params, optFns...)
}

func (g *guardedS3) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if err := g.limiter.Allow(ClassList); err != nil {
		return nil, err
	}
	return g.inner.ListObjectsV2(ctx, params, optFns...)
}
