package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const downloadURLTTL = time.Minute

// WithSignedDownloads configures existing output-object downloads. Empty
// CloudFront settings leave their ingress path unavailable.
func (s *Server) WithSignedDownloads(cache *teamblob.Store, client *s3.Client, domain, keyPairID, privateKey string) error {
	if client == nil {
		return errors.New("signed downloads need an S3 client")
	}
	stores := map[store.StorageKind]*teamblob.Store{}
	if cache != nil {
		stores[store.StorageCache] = cache
	}
	var cdn *sign.URLSigner
	if domain != "" || keyPairID != "" || privateKey != "" {
		if domain == "" || keyPairID == "" || privateKey == "" {
			return errors.New("CloudFront domain, key pair ID and private key must be set together")
		}
		if strings.ContainsAny(domain, "/:@?#") {
			return errors.New("CloudFront domain must be a hostname")
		}
		key, err := sign.LoadPEMPrivKey(strings.NewReader(privateKey))
		if err != nil {
			return fmt.Errorf("CloudFront private key: %w", err)
		}
		cdn = sign.NewURLSigner(keyPairID, key)
	}
	s.downloadStores = stores
	s.downloadS3 = s3.NewPresignClient(client)
	s.downloadCDN = cdn
	s.downloadDomain = domain
	return nil
}

func downloadViaIngress(r *http.Request) bool {
	_, forwardedFor := r.Header["X-Forwarded-For"]
	_, forwardedHost := r.Header["X-Forwarded-Host"]
	return forwardedFor || forwardedHost
}

var errSigningUnavailable = errors.New("download signing unavailable")

func (s *Server) signObjectURL(r *http.Request, bucket *teamblob.Store, objectKey string) (string, time.Time, error) {
	expires := time.Now().Add(downloadURLTTL).UTC()
	if downloadViaIngress(r) {
		if s.downloadCDN == nil {
			return "", time.Time{}, fmt.Errorf("%w: CloudFront signing unavailable", errSigningUnavailable)
		}
		resource := (&url.URL{Scheme: "https", Host: s.downloadDomain, Path: "/" + objectKey}).String()
		policy := &sign.Policy{Statements: []sign.Statement{{Resource: resource, Condition: sign.Condition{DateLessThan: sign.NewAWSEpochTime(expires)}}}}
		signed, err := s.downloadCDN.SignWithPolicy(resource, policy)
		return signed, expires, err
	}
	if s.downloadS3 == nil {
		return "", time.Time{}, fmt.Errorf("%w: S3 signing unavailable", errSigningUnavailable)
	}
	out, err := s.downloadS3.PresignGetObject(r.Context(), &s3.GetObjectInput{Bucket: aws.String(bucket.Bucket()), Key: aws.String(objectKey)}, func(o *s3.PresignOptions) { o.Expires = downloadURLTTL })
	if err != nil {
		return "", time.Time{}, err
	}
	return out.URL, expires, nil
}
