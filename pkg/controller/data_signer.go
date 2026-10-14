package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// FilesystemDataSigner binds filesystem transfers to one method, object and expiry.
// Verify returns transfer constraints; the blob owner verifies bytes before committing a receipt.
type FilesystemDataSigner struct {
	base url.URL
	key  []byte
	now  func() time.Time
}

// NewFilesystemDataSigner uses a deployment signing key shared by all of its controllers.
func NewFilesystemDataSigner(baseURL string, key []byte) (*FilesystemDataSigner, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.RawPath != "" {
		return nil, errors.New("invalid signed-object base URL")
	}
	if len(key) < sha256.Size {
		return nil, errors.New("signed-object key needs at least 256 bits")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	return &FilesystemDataSigner{base: *base, key: append([]byte(nil), key...), now: time.Now}, nil
}

func (s *FilesystemDataSigner) Sign(ctx context.Context, object capability.ObjectToSign) (capability.SignedObject, error) {
	if err := ctx.Err(); err != nil {
		return capability.SignedObject{}, err
	}
	object.ExpiresAt = object.ExpiresAt.UTC()
	if _, err := validateSignedObject(object, s.now()); err != nil {
		return capability.SignedObject{}, err
	}
	signature, err := s.signature(object)
	if err != nil {
		return capability.SignedObject{}, err
	}
	u := s.base
	if object.Method == http.MethodGet {
		u.Path += "/api/v1/data/objects/" + object.Key
	} else {
		u.Path += "/api/v1/data/uploads/" + base64.RawURLEncoding.EncodeToString([]byte(object.Key))
	}
	query := url.Values{
		"expires": {strconv.FormatInt(object.ExpiresAt.UnixNano(), 10)}, "size": {strconv.FormatInt(object.Size, 10)},
		"sha256": {object.SHA256}, "signature": {signature},
	}
	u.RawQuery = query.Encode()
	result := capability.SignedObject{URL: u.String(), SHA256: object.SHA256, Size: object.Size, ExpiresAt: object.ExpiresAt}
	if object.Method == http.MethodPut {
		result.Headers = map[string]string{"Content-Length": strconv.FormatInt(object.Size, 10), "X-Sparkwing-SHA256": object.SHA256}
	}
	return result, nil
}

// Verify accepts only a signed transfer, never a Sparkwing bearer in its place.
func (s *FilesystemDataSigner) Verify(r *http.Request) (capability.ObjectToSign, error) {
	refuse := &apierr.Error{Code: apierr.Unauthenticated, Message: "invalid signed object URL"}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 4 {
		return capability.ObjectToSign{}, refuse
	}
	for _, key := range []string{"expires", "size", "sha256", "signature"} {
		if len(query[key]) != 1 {
			return capability.ObjectToSign{}, refuse
		}
	}
	expires, err := strconv.ParseInt(query.Get("expires"), 10, 64)
	if err != nil {
		return capability.ObjectToSign{}, refuse
	}
	size, err := strconv.ParseInt(query.Get("size"), 10, 64)
	if err != nil {
		return capability.ObjectToSign{}, refuse
	}
	key, err := s.objectKey(r)
	if err != nil {
		return capability.ObjectToSign{}, refuse
	}
	object := capability.ObjectToSign{Method: r.Method, Key: key, Size: size, SHA256: query.Get("sha256"), ExpiresAt: time.Unix(0, expires).UTC()}
	if _, err := validateSignedObject(object, s.now()); err != nil {
		return capability.ObjectToSign{}, refuse
	}
	want, err := s.signature(object)
	if err != nil {
		return capability.ObjectToSign{}, refuse
	}
	if !hmac.Equal([]byte(want), []byte(query.Get("signature"))) {
		return capability.ObjectToSign{}, refuse
	}
	if r.Method == http.MethodPut && (r.ContentLength != size || r.Header.Get("X-Sparkwing-SHA256") != object.SHA256) {
		return capability.ObjectToSign{}, refuse
	}
	return object, nil
}

func (s *FilesystemDataSigner) objectKey(r *http.Request) (string, error) {
	if r.Method == http.MethodGet {
		key, found := strings.CutPrefix(r.URL.Path, s.base.Path+"/api/v1/data/objects/")
		if found {
			return key, nil
		}
	}
	if r.Method == http.MethodPut {
		encoded, found := strings.CutPrefix(r.URL.Path, s.base.Path+"/api/v1/data/uploads/")
		if found {
			key, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
			return string(key), err
		}
	}
	return "", errors.New("invalid object path")
}

func (s *FilesystemDataSigner) signature(object capability.ObjectToSign) (string, error) {
	// safety: structured fields prevent a key containing separators from changing the signed constraints.
	message, err := json.Marshal(struct {
		Origin string
		Object capability.ObjectToSign
	}{s.base.String(), object})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.key)
	if _, err := mac.Write(message); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// S3DataSigner signs the same authorized object constraints for an S3 backend.
type S3DataSigner struct {
	client *s3.PresignClient
	bucket string
	now    func() time.Time
}

// NewS3DataSigner uses the configured bucket; requests cannot select a bucket.
func NewS3DataSigner(client *s3.Client, bucket string) (*S3DataSigner, error) {
	if client == nil || bucket == "" {
		return nil, errors.New("signed objects require an S3 client and bucket")
	}
	return &S3DataSigner{client: s3.NewPresignClient(client), bucket: bucket, now: time.Now}, nil
}

func (s *S3DataSigner) Sign(ctx context.Context, object capability.ObjectToSign) (capability.SignedObject, error) {
	ttl, err := validateSignedObject(object, s.now())
	if err != nil {
		return capability.SignedObject{}, err
	}
	result := capability.SignedObject{SHA256: object.SHA256, Size: object.Size, ExpiresAt: object.ExpiresAt}
	options := func(o *s3.PresignOptions) { o.Expires = ttl }
	if object.Method == http.MethodGet {
		request, err := s.client.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(object.Key)}, options)
		if err != nil {
			return capability.SignedObject{}, err
		}
		result.URL = request.URL
		result.Headers = signedHeaders(request.SignedHeader)
		return result, nil
	}
	digest, err := hex.DecodeString(object.SHA256)
	if err != nil {
		return capability.SignedObject{}, err
	}
	checksum := base64.StdEncoding.EncodeToString(digest)
	request, err := s.client.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(object.Key),
		ContentLength: aws.Int64(object.Size), ChecksumSHA256: aws.String(checksum),
	}, options)
	if err != nil {
		return capability.SignedObject{}, err
	}
	result.URL, result.Headers = request.URL, signedHeaders(request.SignedHeader)
	result.Headers["Content-Length"] = strconv.FormatInt(object.Size, 10)
	result.Headers["x-amz-checksum-sha256"] = checksum
	return result, nil
}

func signedHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if !strings.EqualFold(key, "Host") {
			result[key] = strings.Join(values, ",")
		}
	}
	return result
}

func validateSignedObject(object capability.ObjectToSign, now time.Time) (time.Duration, error) {
	if object.Key == "" || !utf8.ValidString(object.Key) || path.Clean(object.Key) != object.Key || strings.HasPrefix(object.Key, "/") ||
		object.Key == "." || object.Key == ".." || strings.HasPrefix(object.Key, "../") || strings.ContainsAny(object.Key, "\x00\\") || object.Size < 0 {
		return 0, errors.New("invalid signed object key or size")
	}
	digest, err := hex.DecodeString(object.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != object.SHA256 {
		return 0, errors.New("signed object needs a SHA-256 digest")
	}
	maximum := limits.DataDownloadURLTTL
	if object.Method == http.MethodPut {
		maximum = limits.DataUploadURLTTL
	} else if object.Method != http.MethodGet {
		return 0, errors.New("unsupported signed object method")
	}
	ttl := object.ExpiresAt.Sub(now)
	if ttl <= 0 || ttl > maximum {
		return 0, errors.New("signed object expiry exceeds its transfer lifetime")
	}
	return ttl, nil
}

var (
	_ capability.DataSigner = (*FilesystemDataSigner)(nil)
	_ capability.DataSigner = (*S3DataSigner)(nil)
)
