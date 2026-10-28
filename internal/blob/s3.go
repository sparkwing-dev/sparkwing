package blob

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

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
	prefix  string
}

// NewS3 scopes the controller's S3 client to the configured bucket and path prefix.
func NewS3(client *s3.Client, bucket, prefix string) (*S3, error) {
	if client == nil || bucket == "" {
		return nil, fmt.Errorf("S3 requires a client and bucket")
	}
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		if !fs.ValidPath(prefix) || prefix == "." || strings.ContainsRune(prefix, '\\') || strings.ContainsFunc(prefix, unicode.IsControl) {
			return nil, fmt.Errorf("invalid S3 storage prefix")
		}
		prefix += "/"
	}
	presign := s3.NewPresignClient(client, func(o *s3.PresignOptions) {
		o.Presigner = v4.NewSigner(func(s *v4.SignerOptions) { s.DisableHeaderHoisting = true })
	})
	return &S3{client: client, presign: presign, bucket: bucket, prefix: prefix}, nil
}

// PhysicalKey locates a validated backend-relative key under the configured storage prefix.
func (s *S3) PhysicalKey(key string) string { return s.prefix + key }

// Sign binds size and checksum to PUT and names one immutable object for GET.
func (s *S3) Sign(ctx context.Context, object capability.ObjectToSign) (capability.SignedObject, error) {
	now := time.Now()
	if err := validateSigning(object, now); err != nil {
		return capability.SignedObject{}, err
	}
	options := func(o *s3.PresignOptions) { o.Expires = object.ExpiresAt.Sub(now) }
	var signed *v4.PresignedHTTPRequest
	var err error
	if object.Method == http.MethodPut {
		raw, decodeErr := hex.DecodeString(object.SHA256)
		if decodeErr != nil {
			return capability.SignedObject{}, decodeErr
		}
		signed, err = s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(object.Key)), ContentLength: aws.Int64(object.Size),
			ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(raw)),
		}, options)
	} else {
		// safety: the default response-checksum mode signs an x-amz-checksum-mode
		// header, which a bare URL fetch cannot send; readers verify the digest.
		hostOnly := func(o *s3.PresignOptions) {
			o.ClientOptions = append(o.ClientOptions, func(o *s3.Options) { o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired })
		}
		signed, err = s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(object.Key))}, options, hostOnly)
	}
	if err != nil {
		return capability.SignedObject{}, err
	}
	return capability.SignedObject{URL: signed.URL, Headers: signedHeaders(signed.SignedHeader), Size: object.Size, SHA256: object.SHA256, ExpiresAt: object.ExpiresAt}, nil
}

func signedHeaders(signed http.Header) map[string]string {
	headers := make(map[string]string, len(signed))
	for k, values := range signed {
		// safety: the URL supplies Host; net/http ignores a Host header map entry.
		if !strings.EqualFold(k, "Host") {
			headers[k] = strings.Join(values, ",")
		}
	}
	return headers
}

func (s *S3) verify(ctx context.Context, key string, expected Blob) (string, error) {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(key))})
	if s3Error(err, "NoSuchKey", "NotFound") {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if object.Body == nil {
		return "", ErrIntegrity
	}
	if object.ContentLength == nil || *object.ContentLength != expected.Size {
		return "", errors.Join(ErrIntegrity, object.Body.Close())
	}
	if err := verifyObject(ctx, object.Body, expected); err != nil {
		return "", err
	}
	return aws.ToString(object.ETag), nil
}

// CommitObject verifies this ticket's staged bytes before publishing its immutable CAS key.
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
	etag, err := s.verify(ctx, pending, expected)
	if err != nil {
		return err
	}
	if etag == "" {
		return fmt.Errorf("staging object has no version fence")
	}
	_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(final)), CopySource: aws.String(url.PathEscape(s.bucket + "/" + s.PhysicalKey(pending))),
		CopySourceIfMatch: aws.String(etag), IfNoneMatch: aws.String("*"), MetadataDirective: types.MetadataDirectiveReplace,
	})
	if err != nil && !s3Error(err, "PreconditionFailed") {
		return err
	}
	_, err = s.verify(ctx, final, expected)
	return err
}

// DeleteObject removes a key after the caller commits retention or staging cleanup.
func (s *S3) DeleteObject(ctx context.Context, key string) error {
	if err := validateDeleteKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(key))})
	if s3Error(err, "NoSuchKey", "NotFound") {
		return nil
	}
	return err
}

// DeleteBlob removes a CAS object after the leased collector's reachability recheck.
func (s *S3) DeleteBlob(ctx context.Context, object Blob) error {
	key, err := ObjectKey(object.Team, object.Digest)
	if err != nil {
		return err
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(key))})
	if s3Error(err, "NoSuchKey", "NotFound") {
		return nil
	}
	if err != nil {
		return err
	}
	if aws.ToString(head.ETag) == "" {
		return fmt.Errorf("blob collection requires an object ETag")
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(key)), IfMatch: head.ETag})
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
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(key))})
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
