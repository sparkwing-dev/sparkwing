package controller

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) handleDirectCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.directUploads == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"upload": true, "source": s.dataDownloadURL() != "" && (!downloadViaIngress(r) || s.downloadCDN != nil), "download": s.dataDownloadURL() != "" && (!downloadViaIngress(r) || s.downloadCDN != nil)})
}

type directUploadS3 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	prefix  string
}

// WithDirectUploads enables the data upload and commit routes for an S3
// bucket. Prefix is the existing cache namespace inside that bucket.
func (s *Server) WithDirectUploads(client *s3.Client, bucket, prefix string) *Server {
	if client != nil && bucket != "" && prefix != "" {
		s.directUploads = &directUploadS3{
			client: client, presign: s3.NewPresignClient(client,
				func(o *s3.PresignOptions) {
					o.Presigner = v4.NewSigner(func(s *v4.SignerOptions) { s.DisableHeaderHoisting = true })
				}),
			bucket: bucket, prefix: strings.Trim(prefix, "/"),
		}
	}
	return s
}

// DirectUploadResponse tells the client where and how to put exactly the
// declared bytes before it calls /api/v1/data/commit.
type DirectUploadResponse struct {
	UploadID  string            `json:"upload_id"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type directUploadRequest struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	RunID  string `json:"run_id"`
}

type directCommitRequest struct {
	UploadID string `json:"upload_id"`
	RunID    string `json:"run_id"`
}

type directCaller struct {
	team        store.Team
	runID       string
	principal   string
	claimPrefix string
	provenance  string
}

// safety: A signing grant must name an exact live claim so pooled token reuse cannot revive an old claimant.
func (s *Server) directCaller(w http.ResponseWriter, r *http.Request, runID string) (directCaller, bool) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		writeError(w, http.StatusUnauthorized, errors.New("a bearer is required"))
		return directCaller{}, false
	}
	if !strings.HasPrefix(token, authwire.CacheGrantPrefix) {
		writeError(w, http.StatusForbidden, errors.New("direct uploads require a claim-bound cache grant"))
		return directCaller{}, false
	}
	grant, err := authwire.VerifyCacheGrant(os.Getenv(authwire.CacheGrantKeyEnv), token, time.Now())
	if err != nil || grant.Claim == nil {
		writeError(w, http.StatusForbidden, errors.New("the cache grant is not bound to this live claimant"))
		return directCaller{}, false
	}
	if !s.allowDataRequest(w, r, store.Team(grant.Team), grant.Claim.TokenPrefix) {
		return directCaller{}, false
	}
	if (runID != "" && grant.Run != runID) || s.verifyLiveDataGrant(r.Context(), grant, false) != nil {
		writeError(w, http.StatusForbidden, errors.New("the cache grant is not bound to this live claimant"))
		return directCaller{}, false
	}
	metered, err := s.store.TokenMetered(r.Context(), grant.Claim.TokenPrefix)
	if err != nil {
		s.writeInternalError(w, r, "direct upload provenance", err)
		return directCaller{}, false
	}
	provenance := "local"
	if metered {
		provenance = "cloud"
	}
	return directCaller{
		team: store.Team(grant.Team), runID: grant.Run, principal: grant.Claim.Principal,
		claimPrefix: grant.Claim.TokenPrefix, provenance: provenance,
	}, true
}

func (s *Server) directSourceCaller(w http.ResponseWriter, r *http.Request) (directCaller, bool) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		writeError(w, http.StatusUnauthorized, errors.New("source upload needs a bearer"))
		return directCaller{}, false
	}
	principal, err := s.authMiddleware().Authenticate(token)
	if err != nil || principal == nil {
		writeError(w, http.StatusUnauthorized, errors.New("invalid source upload bearer"))
		return directCaller{}, false
	}
	if principal.Kind != store.TokenKindUser || (!principal.HasScope(ScopeRunsWrite) && !principal.HasScope(ScopeAdmin)) {
		writeError(w, http.StatusForbidden, errors.New("source upload needs a user token with runs.write"))
		return directCaller{}, false
	}
	team := store.NormalizeTeam(principal.Team)
	if !teamblob.ValidTeam(string(team)) {
		writeError(w, http.StatusForbidden, errors.New("source upload bearer names no team"))
		return directCaller{}, false
	}
	if !s.allowDataRequest(w, r, team, principal.TokenPrefix) {
		return directCaller{}, false
	}
	return directCaller{team: team, principal: principal.Name, claimPrefix: principal.TokenPrefix, provenance: "local"}, true
}

func (d *directUploadS3) pendingKey(id string) string { return "pending/" + id }
func (d *directUploadS3) finalKey(u store.Upload) string {
	return d.prefix + "/teams/" + string(u.Team) + "/" + u.Provenance + "/" + u.Key
}

func validDirectKey(kind, key, sha string) bool {
	switch kind {
	case "binary":
		input, _, ok := strings.Cut(strings.TrimPrefix(key, "bin/"), "/")
		return ok && validBinaryInput(input) && key == "bin/"+input+"/"+sha
	case "artifact":
		return key == "artifacts/blobs/"+sha || key == "artifacts/manifests/"+sha
	case "source":
		digest, ok := store.SourceKeyDigest(key)
		return ok && digest == sha
	default:
		return false
	}
}

func validBinaryInput(raw string) bool {
	if len(raw) != 17 || raw[8] != '-' {
		return false
	}
	for i, c := range raw {
		if i == 8 {
			continue
		}
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) handleDirectUpload(w http.ResponseWriter, r *http.Request) {
	d := s.directUploads
	if d == nil {
		writeError(w, http.StatusNotFound, errors.New("direct uploads are unavailable"))
		return
	}
	var req directUploadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Kind == "source" && req.RunID != "" {
		writeError(w, http.StatusBadRequest, errors.New("source uploads precede a run"))
		return
	}
	var caller directCaller
	var ok bool
	if req.Kind == "source" {
		caller, ok = s.directSourceCaller(w, r)
	} else {
		caller, ok = s.directCaller(w, r, req.RunID)
	}
	if !ok {
		return
	}
	if req.Size > store.DirectUploadMaxSize {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("direct uploads are limited to 500 MiB"))
		return
	}
	if !validDirectKey(req.Kind, req.Key, req.SHA256) || req.Size < 0 {
		writeError(w, http.StatusBadRequest, errors.New("the upload needs a content-addressed key and a nonnegative size"))
		return
	}
	raw, err := hex.DecodeString(req.SHA256)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("sha256 must be lowercase hex"))
		return
	}
	u, err := s.store.ReserveUpload(r.Context(), store.UploadRequest{
		Team: caller.team, RunID: caller.runID, Kind: store.StorageCache, Key: req.Key, Size: req.Size,
		SHA256: req.SHA256, Principal: caller.principal, ClaimPrefix: caller.claimPrefix, Provenance: caller.provenance,
	})
	if err != nil {
		if s.writeComputeLimitRefusal(w, r, "", "", err) {
			return
		}
		switch {
		case errors.Is(err, store.ErrStorageQuota):
			writeError(w, http.StatusRequestEntityTooLarge, err)
		case errors.Is(err, store.ErrObjectExists):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, store.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, err)
		default:
			s.writeInternalError(w, r, "reserve direct upload", err)
		}
		return
	}
	checksum := base64.StdEncoding.EncodeToString(raw)
	put, err := d.presign.PresignPutObject(r.Context(), &s3.PutObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(d.pendingKey(u.ID)),
		ContentLength: aws.Int64(u.Size), ChecksumSHA256: aws.String(checksum),
	}, func(o *s3.PresignOptions) { o.Expires = 15 * time.Minute })
	if err != nil {
		if releaseErr := s.store.ReleaseStorage(r.Context(), u.Team, u.ID, time.Now()); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
		s.writeInternalError(w, r, "presign direct upload", err)
		return
	}
	headers := map[string]string{"Content-Length": fmt.Sprint(u.Size), "x-amz-checksum-sha256": checksum}
	for k, v := range put.SignedHeader {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	writeJSON(w, http.StatusOK, DirectUploadResponse{
		UploadID: u.ID, URL: put.URL,
		Headers: headers, ExpiresAt: time.Now().Add(15 * time.Minute).UTC(),
	})
}

func (s *Server) handleDirectCommit(w http.ResponseWriter, r *http.Request) {
	d := s.directUploads
	if d == nil {
		writeError(w, http.StatusNotFound, errors.New("direct uploads are unavailable"))
		return
	}
	var req directCommitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	var caller directCaller
	var ok bool
	if strings.EqualFold(scheme, "Bearer") && !strings.HasPrefix(token, authwire.CacheGrantPrefix) {
		caller, ok = s.directSourceCaller(w, r)
	} else {
		caller, ok = s.directCaller(w, r, req.RunID)
	}
	if !ok {
		return
	}
	u, err := s.store.UploadForTeam(r.Context(), caller.team, req.UploadID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "read direct upload", err)
		return
	}
	if strings.HasPrefix(u.Key, "sources/") != (caller.runID == "") ||
		u.RunID != caller.runID || u.Principal != caller.principal || u.ClaimPrefix != caller.claimPrefix || !time.Now().Before(u.ExpiresAt) {
		writeError(w, http.StatusForbidden, errors.New("this claimant cannot commit the upload"))
		return
	}
	if !u.CommittedAt.IsZero() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	head, err := d.client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(d.pendingKey(u.ID)), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey") {
			writeError(w, http.StatusConflict, errors.New("the pending upload is absent"))
		} else {
			s.writeInternalError(w, r, "head pending upload", err)
		}
		return
	}
	raw, err := hex.DecodeString(u.SHA256)
	if err != nil {
		s.writeInternalError(w, r, "decode reserved upload checksum", err)
		return
	}
	if aws.ToInt64(head.ContentLength) != u.Size || aws.ToString(head.ChecksumSHA256) != base64.StdEncoding.EncodeToString(raw) {
		writeError(w, http.StatusUnprocessableEntity, errors.New("pending object size or sha256 checksum does not match the declaration"))
		return
	}
	final := d.finalKey(u)
	recordedUploader := u.Principal
	_, err = d.client.CopyObject(r.Context(), &s3.CopyObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(final),
		CopySource:  aws.String(url.PathEscape(d.bucket + "/" + d.pendingKey(u.ID))),
		IfNoneMatch: aws.String("*"), MetadataDirective: types.MetadataDirectiveReplace,
		Metadata: map[string]string{
			"uploader": u.Principal, "provenance": u.Provenance,
			"sha256": u.SHA256, "upload-id": u.ID,
		},
	})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "PreconditionFailed" {
			previous, herr := d.client.HeadObject(r.Context(), &s3.HeadObjectInput{
				Bucket: aws.String(d.bucket), Key: aws.String(final), ChecksumMode: types.ChecksumModeEnabled,
			})
			if herr != nil || aws.ToInt64(previous.ContentLength) != u.Size ||
				aws.ToString(previous.ChecksumSHA256) != base64.StdEncoding.EncodeToString(raw) ||
				previous.Metadata["sha256"] != u.SHA256 || previous.Metadata["provenance"] != u.Provenance ||
				previous.Metadata["uploader"] == "" ||
				(strings.HasPrefix(u.Key, "sources/") &&
					(previous.Metadata["uploader"] != u.Principal || previous.Metadata["upload-id"] != u.ID)) {
				writeError(w, http.StatusConflict, store.ErrObjectExists)
				return
			}
			recordedUploader = previous.Metadata["uploader"]
		} else {
			s.writeInternalError(w, r, "copy committed object", err)
			return
		}
	}
	if err := s.store.CommitUpload(r.Context(), u.Team, u.ID, recordedUploader, time.Now()); err != nil {
		if errors.Is(err, store.ErrObjectExists) {
			writeError(w, http.StatusConflict, err)
		} else {
			s.writeInternalError(w, r, "commit direct upload", err)
		}
		return
	}
	_, err = d.client.DeleteObject(r.Context(), &s3.DeleteObjectInput{Bucket: aws.String(d.bucket), Key: aws.String(d.pendingKey(u.ID))})
	if err != nil {
		s.logger.Warn("delete committed pending object", "upload_id", u.ID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
