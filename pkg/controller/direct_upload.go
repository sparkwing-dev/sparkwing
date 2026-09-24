package controller

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) handleDirectCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.directUploads == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"upload": true, "download": s.dataDownloadURL() != "" && (!downloadViaIngress(r) || s.downloadCDN != nil)})
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
	var name, prefix string
	var team store.Team
	if strings.HasPrefix(token, authwire.CacheGrantPrefix) {
		g, err := s.verifyDataGrant(r.Context(), token)
		if err != nil || (runID != "" && g.Run != runID) || g.Claim == nil {
			writeError(w, http.StatusForbidden, errors.New("the cache grant is not bound to this live claimant"))
			return directCaller{}, false
		}
		name, prefix, team = g.Claim.Principal, g.Claim.TokenPrefix, store.Team(g.Team)
		runID = g.Run
	} else {
		if runID == "" {
			writeError(w, http.StatusBadRequest, errors.New("run_id is required for this bearer"))
			return directCaller{}, false
		}
		p, err := s.authMiddleware().Authenticate(token)
		if err != nil || p == nil || !(p.HasScope(ScopeNodesClaim) || p.HasScope(ScopeTriggersClaim)) {
			writeError(w, http.StatusUnauthorized, errors.New("this bearer cannot claim run work"))
			return directCaller{}, false
		}
		name, prefix, team = p.Name, p.TokenPrefix, store.NormalizeTeam(p.Team)
	}
	claimed, err := s.store.ClaimedRunFor(r.Context(), runID, store.ClaimIdentity{Principal: name, TokenPrefix: prefix}, time.Now())
	if errors.Is(err, store.ErrNotFound) || (err == nil && claimed.Team != team) {
		writeError(w, http.StatusForbidden, errors.New("this credential holds no live claim on the run"))
		return directCaller{}, false
	}
	if err != nil {
		s.writeInternalError(w, r, "direct upload claim", err)
		return directCaller{}, false
	}
	metered, err := s.store.TokenMetered(r.Context(), prefix)
	if err != nil {
		s.writeInternalError(w, r, "direct upload provenance", err)
		return directCaller{}, false
	}
	provenance := "local"
	if metered {
		provenance = "cloud"
	}
	return directCaller{team: claimed.Team, runID: runID, principal: name, claimPrefix: prefix, provenance: provenance}, true
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
	caller, ok := s.directCaller(w, r, req.RunID)
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
	u, err := s.store.ReserveUpload(r.Context(), store.UploadRequest{
		Team: caller.team, RunID: caller.runID, Kind: store.StorageCache, Key: req.Key, Size: req.Size,
		SHA256: req.SHA256, Principal: caller.principal, ClaimPrefix: caller.claimPrefix, Provenance: caller.provenance,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrStorageQuota):
			writeError(w, http.StatusRequestEntityTooLarge, err)
		case errors.Is(err, store.ErrFreeStoragePaused):
			writeError(w, http.StatusPaymentRequired, err)
		case errors.Is(err, store.ErrObjectExists):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, store.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, err)
		default:
			s.writeInternalError(w, r, "reserve direct upload", err)
		}
		return
	}
	raw, _ := hex.DecodeString(req.SHA256)
	checksum := base64.StdEncoding.EncodeToString(raw)
	put, err := d.presign.PresignPutObject(r.Context(), &s3.PutObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(d.pendingKey(u.ID)),
		ContentLength: aws.Int64(u.Size), ChecksumSHA256: aws.String(checksum),
	}, func(o *s3.PresignOptions) { o.Expires = 15 * time.Minute })
	if err != nil {
		_ = s.store.ReleaseStorage(r.Context(), u.Team, u.ID, time.Now())
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
	caller, ok := s.directCaller(w, r, req.RunID)
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
	if u.RunID != caller.runID || u.Principal != caller.principal || u.ClaimPrefix != caller.claimPrefix || !time.Now().Before(u.ExpiresAt) {
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
	raw, _ := hex.DecodeString(u.SHA256)
	if aws.ToInt64(head.ContentLength) != u.Size || aws.ToString(head.ChecksumSHA256) != base64.StdEncoding.EncodeToString(raw) {
		writeError(w, http.StatusUnprocessableEntity, errors.New("pending object size or sha256 checksum does not match the declaration"))
		return
	}
	final := d.finalKey(u)
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
				previous.Metadata["sha256"] != u.SHA256 || previous.Metadata["provenance"] != u.Provenance {
				writeError(w, http.StatusConflict, store.ErrObjectExists)
				return
			}
		} else {
			s.writeInternalError(w, r, "copy committed object", err)
			return
		}
	}
	if err := s.store.CommitUpload(r.Context(), u.Team, u.ID, time.Now()); err != nil {
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
