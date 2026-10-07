package controller

import (
	"context"
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
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
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

func (s *Server) verifyLiveDataGrant(ctx context.Context, grant authwire.CacheGrant, allowPendingTrigger bool) (bool, error) {
	if grant.Claim == nil {
		return false, errors.New("cache grant has no claim")
	}
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

func (s *Server) downloadTeam(w http.ResponseWriter, r *http.Request, kind string) (store.Team, *authwire.CacheGrant, bool) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		writeError(w, http.StatusUnauthorized, errors.New("download needs a bearer"))
		return "", nil, false
	}
	if strings.HasPrefix(token, authwire.CacheGrantPrefix) {
		grant, err := authwire.VerifyCacheGrant(os.Getenv(authwire.CacheGrantKeyEnv), token, time.Now())
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return "", nil, false
		}
		if grant.Claim == nil {
			writeError(w, http.StatusForbidden, errors.New("cache grant has no claim"))
			return "", nil, false
		}
		if !s.allowDataRequest(w, r, store.Team(grant.Team), grant.Claim.TokenPrefix) {
			return "", nil, false
		}
		if kind == "log" {
			writeError(w, http.StatusForbidden, errors.New("cache grants cannot sign log downloads"))
			return "", nil, false
		}
		if _, err := s.verifyLiveDataGrant(r.Context(), grant, kind == "source" || kind == "binary"); err != nil {
			writeError(w, http.StatusForbidden, err)
			return "", nil, false
		}
		return store.Team(grant.Team), &grant, true
	}
	p, err := s.authMiddleware().Authenticate(token)
	if err != nil || p == nil {
		writeError(w, http.StatusUnauthorized, errors.New("invalid download bearer"))
		return "", nil, false
	}
	if kind == "source" {
		writeError(w, http.StatusForbidden, errors.New("source downloads need the run's live claim grant"))
		return "", nil, false
	}
	requiredScope := ScopeRunsRead
	if kind == "log" {
		requiredScope = ScopeLogsRead
	}
	if !p.HasScope(requiredScope) && !p.HasScope(ScopeAdmin) {
		writeError(w, http.StatusForbidden, fmt.Errorf("download needs %s", requiredScope))
		return "", nil, false
	}
	if p.Kind == store.TokenKindRunner && !p.HasScope(ScopeAdmin) {
		writeError(w, http.StatusForbidden, errors.New("runner download needs a claim-bound cache grant"))
		return "", nil, false
	}
	team := store.NormalizeTeam(p.Team)
	if !teamblob.ValidTeam(string(team)) {
		writeError(w, http.StatusForbidden, errors.New("credential names no team"))
		return "", nil, false
	}
	if !s.allowDataRequest(w, r, team, p.TokenPrefix) {
		return "", nil, false
	}
	return team, nil, true
}

func downloadKind(kind string) store.StorageKind {
	switch kind {
	case "binary", "artifact", "source":
		return store.StorageCache
	case "log":
		return store.StorageLogs
	default:
		return ""
	}
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

func (s *Server) handleDataDownload(w http.ResponseWriter, r *http.Request) {
	if len(s.downloadStores) == 0 {
		http.NotFound(w, r)
		return
	}
	var req dataDownloadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	team, grant, ok := s.downloadTeam(w, r, req.Kind)
	if !ok {
		return
	}
	kind := downloadKind(req.Kind)
	bucket := s.downloadStores[kind]
	if bucket == nil || strings.HasPrefix(req.Key, "teams/") ||
		(req.Kind == "artifact" && !strings.HasPrefix(req.Key, "artifacts/")) {
		writeError(w, http.StatusBadRequest, errors.New("invalid download kind or key"))
		return
	}
	var err error
	var cloudReader bool
	if grant != nil {
		cloudReader, err = s.store.TokenMetered(r.Context(), grant.Claim.TokenPrefix)
		if err != nil {
			s.writeInternalError(w, r, "read download provenance", err)
			return
		}
		cloudReader = cloudReader || grant.Claim.Kind == authwire.CacheClaimToken
	}
	var objectSize int64
	var digest string
	var objectKey string
	switch {
	case req.Kind == "source":
		if grant == nil {
			writeError(w, http.StatusForbidden, errors.New("source downloads need a live claim grant"))
			return
		}
		var trigger *store.Trigger
		tenant, findErr := s.tenantForTeam(r.Context(), team)
		if findErr == nil {
			trigger, findErr = tenant.GetTrigger(r.Context(), grant.Run)
		}
		if findErr != nil || trigger.Team != team || !strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@") ||
			req.Key != trigger.TriggerEnv[bincache.SourceBundleObjectEnvKey] {
			writeError(w, http.StatusForbidden, errors.New("source bundle does not belong to this run"))
			return
		}
		keyDigest, valid := store.SourceKeyDigest(req.Key)
		if !valid {
			writeError(w, http.StatusForbidden, errors.New("invalid source bundle key"))
			return
		}
		bound, findErr := s.store.SourceBoundToRun(r.Context(), team, req.Key, grant.Run)
		if findErr != nil || !bound {
			writeError(w, http.StatusForbidden, errors.New("source bundle is not bound to this run"))
			return
		}
		obj, findErr := s.store.CommittedObject(r.Context(), team, req.Key)
		if errors.Is(findErr, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if findErr != nil {
			s.writeInternalError(w, r, "read committed source bundle", findErr)
			return
		}
		if obj.Kind != store.StorageCache || obj.Provenance != "local" || obj.SHA256 != keyDigest {
			writeError(w, http.StatusForbidden, errors.New("source bundle is not committed for this run"))
			return
		}
		objectSize, digest = obj.Size, obj.SHA256
		objectKey, err = bucket.Key(string(team), obj.Provenance+"/"+obj.Key)
	case req.Kind == "binary" && strings.HasPrefix(req.Key, "bin/"):
		repo, refs := "", []string{""}
		if grant != nil {
			if repo, _, refs, err = s.cacheScope(r.Context(), *grant); err != nil {
				writeError(w, http.StatusForbidden, errors.New("the cache grant's run names no cache scope"))
				return
			}
		}
		obj, findErr := s.store.BinaryObject(r.Context(), team, strings.TrimPrefix(req.Key, "bin/"), cloudReader, repo, refs)
		if errors.Is(findErr, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if findErr != nil {
			writeError(w, http.StatusBadRequest, findErr)
			return
		}
		objectSize, digest = obj.Size, obj.SHA256
		objectKey, err = bucket.Key(string(team), obj.Provenance+"/"+obj.Key)
	case req.Kind == "artifact" && strings.HasPrefix(req.Key, "artifacts/"):
		obj, findErr := s.store.CommittedObjectFor(r.Context(), team, req.Key, cloudReader)
		if errors.Is(findErr, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if findErr != nil {
			s.writeInternalError(w, r, "read committed artifact", findErr)
			return
		}
		if obj.Kind != store.StorageCache {
			writeError(w, http.StatusBadRequest, errors.New("object kind does not match"))
			return
		}
		objectSize, digest = obj.Size, obj.SHA256
		objectKey, err = bucket.Key(string(team), obj.Provenance+"/"+obj.Key)
	default:
		if req.Kind == "binary" && (!strings.HasPrefix(req.Key, "bins/") || (cloudReader && s.directUploads != nil)) {
			http.NotFound(w, r)
			return
		}
		// safety: the cache writes a grant's binaries under its run's scope, so a
		// grant reads the scopes it would read there, in the same order.
		scopes := []string{""}
		if grant != nil {
			scopes = grant.ScopePrefixes()
		}
		var object teamblob.Object
		headErr := teamblob.ErrNotFound
		for _, scope := range scopes {
			objectKey, err = bucket.Key(string(team), scope+req.Key)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if object, headErr = bucket.Head(r.Context(), string(team), scope+req.Key); !errors.Is(headErr, teamblob.ErrNotFound) {
				break
			}
		}
		if errors.Is(headErr, teamblob.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if headErr != nil {
			s.writeInternalError(w, r, "head download object", headErr)
			return
		}
		objectSize, digest = object.Size, object.Metadata["sha256"]
	}
	if err != nil || objectSize < 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid download object key or size"))
		return
	}
	if req.Kind == "binary" {
		b, derr := hex.DecodeString(digest)
		if derr != nil || len(b) != sha256.Size {
			s.writeInternalError(w, r, "head download digest", errors.New("binary digest unavailable"))
			return
		}
	}
	signed, expires, err := s.signObjectURL(r, bucket, objectKey)
	if errors.Is(err, errSigningUnavailable) {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "sign download", err)
		return
	}
	// safety: log reads are never counted against a team's download cap, so
	// a team at its cap can still read why its runs failed.
	if req.Kind != "log" {
		_, err = s.store.ChargeDownload(r.Context(), store.DownloadCharge{Team: team, Bytes: objectSize, Now: time.Now(), FreeCapBytes: s.downloadFree, FundedCapBytes: s.downloadFunded})
	}
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
	writeJSON(w, http.StatusOK, DataDownloadResponse{URL: signed, SHA256: digest, Size: objectSize, Expires: expires})
}
