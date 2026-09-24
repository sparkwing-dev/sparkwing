package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	URL     string    `json:"url"`
	SHA256  string    `json:"sha256"`
	Size    int64     `json:"size"`
	Expires time.Time `json:"expires"`
}

// WithSignedDownloads enables one-object download signing for the cache and
// logs buckets. Empty CloudFront settings leave the ingress path unavailable.
func (s *Server) WithSignedDownloads(cache, logs *teamblob.Store, client *s3.Client, domain, keyPairID, privateKey string) error {
	if client == nil {
		return errors.New("signed downloads need an S3 client")
	}
	stores := map[store.StorageKind]*teamblob.Store{}
	if cache != nil {
		stores[store.StorageCache] = cache
	}
	if logs != nil {
		stores[store.StorageLogs] = logs
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
	if s.downloadStores[store.StorageCache] == nil {
		return ""
	}
	return "/api/v1/data/download"
}

func downloadViaIngress(r *http.Request) bool {
	_, forwardedFor := r.Header["X-Forwarded-For"]
	_, forwardedHost := r.Header["X-Forwarded-Host"]
	return forwardedFor || forwardedHost
}

type dataDownloadRequest struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

func (s *Server) downloadTeam(w http.ResponseWriter, r *http.Request) (store.Team, bool) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		writeError(w, http.StatusUnauthorized, errors.New("download needs a bearer"))
		return "", false
	}
	if strings.HasPrefix(token, authwire.CacheGrantPrefix) {
		grant, err := authwire.VerifyCacheGrant(os.Getenv(authwire.CacheGrantKeyEnv), token, time.Now())
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return "", false
		}
		t, err := s.store.ForTeam(r.Context(), store.Team(grant.Team))
		if err != nil {
			s.writeInternalError(w, r, "resolve download team", err)
			return "", false
		}
		run, err := t.GetRun(r.Context(), grant.Run)
		if errors.Is(err, store.ErrNotFound) || err == nil && (run.Status != "running" || run.FinishedAt != nil) {
			writeError(w, http.StatusForbidden, errors.New("cache grant run is not live"))
			return "", false
		}
		if err != nil {
			s.writeInternalError(w, r, "check download run", err)
			return "", false
		}
		if grant.Claim == nil {
			writeError(w, http.StatusForbidden, errors.New("cache grant has no live claim"))
			return "", false
		}
		claim := grant.Claim
		identity := store.ClaimIdentity{Principal: claim.Principal, TokenPrefix: claim.TokenPrefix}
		var live bool
		switch claim.Kind {
		case "trigger":
			live, err = s.store.TriggerClaimFenceIsLive(r.Context(), grant.Run, identity, claim.Generation, time.Now())
		case "node":
			fence := store.NodeClaimFence{Claimant: identity, HolderID: claim.HolderID, MembershipID: claim.MembershipID, ReservationID: claim.ReservationID, ClaimGeneration: claim.Generation}
			live, err = s.store.NodeClaimFenceIsLive(r.Context(), grant.Run, claim.NodeID, fence, time.Now())
		}
		if err != nil {
			s.writeInternalError(w, r, "check download claim", err)
			return "", false
		}
		if !live {
			writeError(w, http.StatusForbidden, errors.New("cache grant claim is not live"))
			return "", false
		}
		return store.Team(grant.Team), true
	}
	p, err := s.authMiddleware().Authenticate(token)
	if err != nil || p == nil {
		writeError(w, http.StatusUnauthorized, errors.New("invalid download bearer"))
		return "", false
	}
	if !p.HasScope(ScopeRunsRead) && !p.HasScope(ScopeAdmin) {
		writeError(w, http.StatusForbidden, errors.New("download needs runs.read"))
		return "", false
	}
	if p.Kind == store.TokenKindRunner && !p.HasScope(ScopeAdmin) {
		writeError(w, http.StatusForbidden, errors.New("runner download needs a claim-bound cache grant"))
		return "", false
	}
	team := store.NormalizeTeam(p.Team)
	if !teamblob.ValidTeam(string(team)) {
		writeError(w, http.StatusForbidden, errors.New("credential names no team"))
		return "", false
	}
	return team, true
}

func downloadKind(kind string) store.StorageKind {
	switch kind {
	case "binary", "artifact":
		return store.StorageCache
	case "log":
		return store.StorageLogs
	default:
		return ""
	}
}

func (s *Server) handleDataDownload(w http.ResponseWriter, r *http.Request) {
	if len(s.downloadStores) == 0 {
		http.NotFound(w, r)
		return
	}
	team, ok := s.downloadTeam(w, r)
	if !ok {
		return
	}
	var req dataDownloadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	kind := downloadKind(req.Kind)
	bucket := s.downloadStores[kind]
	if bucket == nil || strings.HasPrefix(req.Key, "teams/") {
		writeError(w, http.StatusBadRequest, errors.New("invalid download kind or key"))
		return
	}
	if req.Kind == "binary" && !strings.HasPrefix(req.Key, "bins/") {
		writeError(w, http.StatusBadRequest, errors.New("binary key must start with bins/"))
		return
	}
	key, err := bucket.Key(string(team), req.Key)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, err := bucket.Head(r.Context(), string(team), req.Key)
	if errors.Is(err, teamblob.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "head download object", err)
		return
	}
	if object.Size < 0 {
		s.writeInternalError(w, r, "head download object", errors.New("negative object size"))
		return
	}
	digest := object.Metadata["sha256"]
	if req.Kind == "binary" {
		b, derr := hex.DecodeString(digest)
		if derr != nil || len(b) != sha256.Size {
			s.writeInternalError(w, r, "head download digest", errors.New("binary digest unavailable"))
			return
		}
	}
	expires := time.Now().Add(downloadURLTTL).UTC()
	var signed string
	if downloadViaIngress(r) {
		if s.downloadCDN == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("CloudFront signing unavailable"))
			return
		}
		resource := (&url.URL{Scheme: "https", Host: s.downloadDomain, Path: "/" + key}).String()
		policy := &sign.Policy{Statements: []sign.Statement{{Resource: resource, Condition: sign.Condition{DateLessThan: sign.NewAWSEpochTime(expires)}}}}
		signed, err = s.downloadCDN.SignWithPolicy(resource, policy)
	} else {
		if s.downloadS3 == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("S3 signing unavailable"))
			return
		}
		out, signErr := s.downloadS3.PresignGetObject(r.Context(), &s3.GetObjectInput{Bucket: aws.String(bucket.Bucket()), Key: aws.String(key)}, func(o *s3.PresignOptions) { o.Expires = downloadURLTTL })
		err = signErr
		if err == nil {
			signed = out.URL
		}
	}
	if err != nil {
		s.writeInternalError(w, r, "sign download", err)
		return
	}
	_, err = s.store.ChargeDownload(r.Context(), store.DownloadCharge{Team: team, Bytes: object.Size, Now: time.Now(), FreeCapBytes: s.downloadFree, FundedCapBytes: s.downloadFunded})
	var capErr *store.DownloadCapError
	switch {
	case errors.As(err, &capErr):
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(capErr.RetryAfter), 10))
		writeError(w, http.StatusTooManyRequests, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "charge signed download", err)
		return
	}
	writeJSON(w, http.StatusOK, DataDownloadResponse{URL: signed, SHA256: digest, Size: object.Size, Expires: expires})
}
