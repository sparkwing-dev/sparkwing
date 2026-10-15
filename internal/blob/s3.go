package blob

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// S3 signs direct transfers and conditionally copies verified staging objects to CAS.
// Provenance and uploader identity belong to records and receipts, because identical
// bytes may be uploaded by multiple principals with different trust.
type S3 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

// NewS3 uses the controller's S3 client and bucket, without a cache-service protocol.
func NewS3(client *s3.Client, bucket string) (*S3, error) {
	if client == nil || bucket == "" {
		return nil, fmt.Errorf("S3 requires a client and bucket")
	}
	presign := s3.NewPresignClient(client, func(o *s3.PresignOptions) {
		o.Presigner = v4.NewSigner(func(s *v4.SignerOptions) { s.DisableHeaderHoisting = true })
	})
	return &S3{client: client, presign: presign, bucket: bucket}, nil
}

// Sign binds size and checksum to PUT and names one immutable object for GET.
func (s *S3) Sign(ctx context.Context, object capability.ObjectToSign) (capability.SignedObject, error) {
	now := time.Now()
	if err := validateObject(object, now); err != nil {
		return capability.SignedObject{}, err
	}
	options := func(o *s3.PresignOptions) { o.Expires = object.ExpiresAt.Sub(now) }
	var signed *v4.PresignedHTTPRequest
	var err error
	if object.Method == http.MethodPut {
		raw, _ := hex.DecodeString(object.SHA256)
		signed, err = s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s.bucket), Key: aws.String(object.Key), ContentLength: aws.Int64(object.Size),
			ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(raw)),
		}, options)
	} else {
		signed, err = s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(object.Key)}, options)
	}
	if err != nil {
		return capability.SignedObject{}, err
	}
	headers := make(map[string]string, len(signed.SignedHeader))
	for k, values := range signed.SignedHeader {
		// safety: the URL supplies Host; net/http ignores a Host header map entry.
		if !strings.EqualFold(k, "Host") {
			headers[k] = strings.Join(values, ",")
		}
	}
	return capability.SignedObject{URL: signed.URL, Headers: headers, Size: object.Size, SHA256: object.SHA256, ExpiresAt: object.ExpiresAt}, nil
}

func (s *S3) head(ctx context.Context, key string, expected Blob) (*s3.HeadObjectOutput, error) {
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled,
	})
	if s3Error(err, "NoSuchKey", "NotFound") {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	raw, _ := hex.DecodeString(string(expected.Digest))
	if head.ContentLength == nil || *head.ContentLength != expected.Size || aws.ToString(head.ChecksumSHA256) != base64.StdEncoding.EncodeToString(raw) {
		return nil, ErrIntegrity
	}
	return head, nil
}

// CommitObject seals verified bytes under a key that cannot overwrite another object.
// An absent staging object permits retry only if the final object verifies in full.
func (s *S3) CommitObject(ctx context.Context, pending string, expected Blob) error {
	final, err := ObjectKey(expected.Team, expected.Digest)
	if err != nil {
		return err
	}
	if expected.Size < 0 {
		return ErrIntegrity
	}
	if err := validatePending(pending, expected.Team); err != nil {
		return err
	}
	head, err := s.head(ctx, pending, expected)
	if errors.Is(err, ErrNotFound) {
		_, err = s.head(ctx, final, expected)
		return err
	}
	if err != nil {
		return err
	}
	if aws.ToString(head.ETag) == "" {
		return fmt.Errorf("staging object has no version fence")
	}
	_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(final), CopySource: aws.String(url.PathEscape(s.bucket + "/" + pending)),
		CopySourceIfMatch: head.ETag, IfNoneMatch: aws.String("*"), MetadataDirective: types.MetadataDirectiveReplace,
	})
	if err != nil && !s3Error(err, "PreconditionFailed") {
		return err
	}
	_, err = s.head(ctx, final, expected)
	return err
}

// DeleteObject removes a key after the caller commits retention or staging cleanup.
func (s *S3) DeleteObject(ctx context.Context, key string) error {
	if err := validateDeleteKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if s3Error(err, "NoSuchKey", "NotFound") {
		return nil
	}
	return err
}

// Get opens an exact log key for a controller-authorized read. The committed
// reader verifies the recorded digest and range before emitting any bytes.
// Listings and signed downloads never authorize log access.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit, err := logObjectLimit(key)
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
	if object.ContentLength == nil || *object.ContentLength < 0 || *object.ContentLength > limit {
		return nil, errors.Join(ErrIntegrity, object.Body.Close())
	}
	return object.Body, nil
}

func s3Error(err error, codes ...string) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	for _, code := range codes {
		if api.ErrorCode() == code {
			return true
		}
	}
	return false
}

var _ ObjectBackend = (*S3)(nil)
