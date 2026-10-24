package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type dataIngressKey struct{}

func dataDownloadContext(r *http.Request) context.Context {
	_, forwardedFor := r.Header["X-Forwarded-For"]
	_, forwardedHost := r.Header["X-Forwarded-Host"]
	if forwardedFor || forwardedHost {
		return context.WithValue(r.Context(), dataIngressKey{}, true)
	}
	return r.Context()
}

type cloudFrontDataBackend struct {
	*blob.S3
	signer *sign.URLSigner
	domain string
}

// CloudFrontDataConfig holds validated download signing configuration.
type CloudFrontDataConfig struct {
	signer *sign.URLSigner
	domain string
}

// ParseCloudFrontDataConfig validates configuration without opening object storage.
func ParseCloudFrontDataConfig(domain, keyPairID, privateKey string) (*CloudFrontDataConfig, error) {
	if domain == "" || keyPairID == "" || privateKey == "" {
		return nil, errors.New("CloudFront requires an S3 backend, domain, key pair ID and private key")
	}
	origin, err := url.Parse("https://" + domain)
	if err != nil || origin.Host != domain || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || strings.ContainsAny(domain, "/:@?#\\ \t\r\n") {
		return nil, errors.New("CloudFront domain must be a hostname")
	}
	if strings.ContainsAny(keyPairID, "&=#? \t\r\n") {
		return nil, errors.New("invalid CloudFront key pair ID")
	}
	key, err := sign.LoadPEMPrivKey(strings.NewReader(privateKey))
	if err != nil {
		return nil, errors.New("invalid CloudFront private key")
	}
	return &CloudFrontDataConfig{signer: sign.NewURLSigner(keyPairID, key), domain: domain}, nil
}

// Backend signs forwarded downloads from objects through CloudFront.
func (c *CloudFrontDataConfig) Backend(objects *blob.S3) (blob.ObjectBackend, error) {
	if objects == nil {
		return nil, errors.New("CloudFront requires an S3 backend")
	}
	return &cloudFrontDataBackend{S3: objects, signer: c.signer, domain: c.domain}, nil
}

func (b *cloudFrontDataBackend) Sign(ctx context.Context, object capability.ObjectToSign) (capability.SignedObject, error) {
	if err := ctx.Err(); err != nil {
		return capability.SignedObject{}, err
	}
	signed, err := b.S3.Sign(ctx, object)
	ingress, _ := ctx.Value(dataIngressKey{}).(bool)
	if err != nil || object.Method != http.MethodGet || !ingress {
		return signed, err
	}
	resource := (&url.URL{Scheme: "https", Host: b.domain, Path: "/" + b.PhysicalKey(object.Key)}).String()
	policy := &sign.Policy{Statements: []sign.Statement{{Resource: resource, Condition: sign.Condition{DateLessThan: sign.NewAWSEpochTime(object.ExpiresAt)}}}}
	signed.URL, err = b.signer.SignWithPolicy(resource, policy)
	if err != nil {
		return capability.SignedObject{}, err
	}
	signed.Headers = map[string]string{}
	return signed, nil
}
