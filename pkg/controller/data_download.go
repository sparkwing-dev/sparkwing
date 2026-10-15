package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const downloadURLTTL = time.Minute

// DataDownloadResponse describes one authorized object and its short-lived URL.
type DataDownloadResponse struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	SHA256  string            `json:"sha256"`
	Size    int64             `json:"size"`
	Expires time.Time         `json:"expires"`
}

// WithSignedDownloads enables one-object download signing for the cache
// bucket. Empty CloudFront settings leave the ingress path unavailable.
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

func (s *Server) dataDownloadURL() string {
	if s.dataController == nil || s.dataController.ResolveAuthority == nil || s.dataController.ReadBlob == nil ||
		s.dataController.Access == nil || s.dataController.Objects == nil || s.dataController.ChargeDownload == nil {
		return ""
	}
	return "/api/v1/data/download"
}

func downloadViaIngress(r *http.Request) bool {
	_, forwardedFor := r.Header["X-Forwarded-For"]
	_, forwardedHost := r.Header["X-Forwarded-Host"]
	return forwardedFor || forwardedHost
}

func (s *Server) verifyLiveDataGrant(ctx context.Context, grant authwire.CacheGrant, allowPendingTrigger bool) (bool, error) {
	if c := grant.Claim; c.Kind == authwire.CacheClaimToken {
		tok, err := s.store.CheckClaimSensitive(ctx, store.ClaimToken{
			Team: store.Team(grant.Team), RunID: grant.Run,
			NodeID: c.NodeID, Generation: c.Generation,
		}, time.Now())
		if err != nil || tok.Prefix != c.TokenPrefix {
			return false, errors.New("cache grant claim is not live")
		}
		// safety: a plan claim uploads only the binary it compiled, as a
		// trigger holder does before dispatch.
		return tok.Kind == store.ClaimTokenPlan, nil
	}
	team, err := s.store.ForTeam(ctx, store.Team(grant.Team))
	if err != nil {
		return false, err
	}
	run, err := team.GetRun(ctx, grant.Run)
	// safety: a live trigger fetches source and compiles binary cache before dispatch.
	pendingTrigger := allowPendingTrigger && grant.Claim.Kind == "trigger" && run != nil && run.Status == "pending"
	if err != nil || run == nil || (run.Status != "running" && !pendingTrigger) || run.FinishedAt != nil {
		return false, errors.New("cache grant run or claim is not live")
	}
	claim := grant.Claim
	// safety: the signed grant can outlive the token that held its claim.
	claimantToken, err := s.store.LookupTokenByPrefix(claim.TokenPrefix)
	if err != nil || claimantToken == nil || claimantToken.Team != store.Team(grant.Team) ||
		claimantToken.Principal != claim.Principal || !claimantToken.IsValid(time.Now()) {
		return false, errors.New("cache grant claimant token is not active")
	}
	identity := store.ClaimIdentity{Principal: claim.Principal, TokenPrefix: claim.TokenPrefix}
	var live bool
	switch claim.Kind {
	case "trigger":
		live, err = s.store.TriggerClaimFenceIsLive(ctx, grant.Run, identity, claim.Generation, time.Now())
	case "node":
		fence := store.NodeClaimFence{Claimant: identity, HolderID: claim.HolderID, MembershipID: claim.MembershipID, ReservationID: claim.ReservationID, ClaimGeneration: claim.Generation}
		live, err = s.store.NodeClaimFenceIsLive(ctx, grant.Run, claim.NodeID, fence, time.Now())
	default:
		return false, errors.New("cache grant claim kind is invalid")
	}
	if err != nil {
		return false, err
	}
	if !live {
		return false, errors.New("cache grant claim is not live")
	}
	return pendingTrigger, nil
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
